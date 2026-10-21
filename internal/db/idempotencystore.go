package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/idempotencyqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/idempotency"
)

// claimAttempts bounds the claim/read loop. A second pass is only needed when a cleanup or a lease
// takeover removes the row between the two statements, so three is generous.
const claimAttempts = 3

const (
	stateInProgress = "in_progress"
	stateCompleted  = "completed"
)

// IdempotencyStore implements idempotency.Store on PostgreSQL (S2-09). Every statement is scoped to
// the user and key in SQL, and each call is its own short transaction.
type IdempotencyStore struct{ database Beginner }

var _ idempotency.Store = (*IdempotencyStore)(nil)

// NewIdempotencyStore accepts a pool or lazy transaction factory from the composition root.
func NewIdempotencyStore(database Beginner) *IdempotencyStore {
	return &IdempotencyStore{database: database}
}

// Claim implements idempotency.Store.
func (s *IdempotencyStore) Claim(ctx context.Context, userID, key string, requestHash []byte) (idempotency.Claim, error) {
	owner, id, err := idempotencyIDs(userID, key)
	if err != nil {
		return idempotency.Claim{}, err
	}
	for range claimAttempts {
		var claim idempotency.Claim
		var done bool
		err := s.write(ctx, func(q *idempotencyqueries.Queries) error {
			var err error
			claim, done, err = claimOnce(ctx, q, owner, id, requestHash)
			return err
		})
		if err != nil {
			return idempotency.Claim{}, err
		}
		if done {
			return claim, nil
		}
	}
	return idempotency.Claim{}, errors.New("claim idempotency key: the record kept changing")
}

// claimOnce tries to take the key and otherwise reads why it could not. done is false when the row
// vanished between the two statements and the caller should try again.
func claimOnce(ctx context.Context, q *idempotencyqueries.Queries, owner, id pgtype.UUID, hash []byte) (claim idempotency.Claim, done bool, err error) {
	_, err = q.ClaimIdempotencyKey(ctx, idempotencyqueries.ClaimIdempotencyKeyParams{
		UserID: owner, Key: id, RequestHash: hash,
		LeaseSeconds: idempotency.ClaimLease.Seconds(), TtlSeconds: idempotency.KeyTTL.Seconds(),
	})
	if err == nil {
		return idempotency.Claim{Outcome: idempotency.Acquired}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Claim{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	row, err := q.GetIdempotencyKey(ctx, idempotencyqueries.GetIdempotencyKeyParams{UserID: owner, Key: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Claim{}, false, nil
	}
	if err != nil {
		return idempotency.Claim{}, false, fmt.Errorf("read idempotency key: %w", err)
	}
	switch {
	case !bytes.Equal(row.RequestHash, hash):
		return idempotency.Claim{Outcome: idempotency.Mismatch}, true, nil
	case row.State == stateCompleted && row.ResponseStatus.Valid:
		return idempotency.Claim{Outcome: idempotency.Replay, Response: idempotency.Response{
			Status: int(row.ResponseStatus.Int16), ContentType: row.ResponseContentType.String, Body: row.ResponseBody,
		}}, true, nil
	case row.State == stateInProgress:
		return idempotency.Claim{Outcome: idempotency.InProgress}, true, nil
	default:
		return idempotency.Claim{}, false, fmt.Errorf("idempotency key is in state %q", row.State)
	}
}

// Complete implements idempotency.Store.
func (s *IdempotencyStore) Complete(ctx context.Context, userID, key string, requestHash []byte, r idempotency.Response) error {
	owner, id, err := idempotencyIDs(userID, key)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q *idempotencyqueries.Queries) error {
		n, err := q.CompleteIdempotencyKey(ctx, idempotencyqueries.CompleteIdempotencyKeyParams{
			UserID: owner, Key: id, RequestHash: requestHash,
			ResponseStatus:      pgtype.Int2{Int16: int16(r.Status), Valid: true}, //nolint:gosec // G115: the middleware stores 2xx statuses only.
			ResponseContentType: pgtype.Text{String: r.ContentType, Valid: true},
			ResponseBody:        nonNil(r.Body),
		})
		if err != nil {
			return fmt.Errorf("complete idempotency key: %w", err)
		}
		if n == 0 {
			return idempotency.ErrClaimLost
		}
		return nil
	})
}

// Release implements idempotency.Store.
func (s *IdempotencyStore) Release(ctx context.Context, userID, key string, requestHash []byte) error {
	owner, id, err := idempotencyIDs(userID, key)
	if err != nil {
		return err
	}
	return s.write(ctx, func(q *idempotencyqueries.Queries) error {
		if _, err := q.ReleaseIdempotencyKey(ctx, idempotencyqueries.ReleaseIdempotencyKeyParams{UserID: owner, Key: id, RequestHash: requestHash}); err != nil {
			return fmt.Errorf("release idempotency key: %w", err)
		}
		return nil
	})
}

// PurgeExpired deletes expired records in batches of batchSize and returns how many it deleted.
// The scheduled cleanup calls it (cmd/idempotency-purge).
func (s *IdempotencyStore) PurgeExpired(ctx context.Context, batchSize int32) (int64, error) {
	if batchSize <= 0 {
		return 0, errors.New("purge idempotency keys: batch size must be positive")
	}
	var total int64
	for {
		var n int64
		err := s.write(ctx, func(q *idempotencyqueries.Queries) error {
			var err error
			n, err = q.PurgeExpiredIdempotencyKeys(ctx, batchSize)
			if err != nil {
				return fmt.Errorf("purge idempotency keys: %w", err)
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batchSize) {
			return total, nil
		}
	}
}

func (s *IdempotencyStore) write(ctx context.Context, fn func(*idempotencyqueries.Queries) error) error {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin idempotency transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(idempotencyqueries.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit idempotency transaction: %w", err)
	}
	return nil
}

func idempotencyIDs(userID, key string) (owner, id pgtype.UUID, err error) {
	if owner, err = parseUUID(userID); err != nil {
		return owner, id, fmt.Errorf("idempotency user: %w", err)
	}
	if id, err = parseUUID(key); err != nil {
		return owner, id, fmt.Errorf("idempotency key: %w", err)
	}
	return owner, id, nil
}

// nonNil keeps an empty body as an empty value, not NULL, which the table forbids for a completed row.
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
