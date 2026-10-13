package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// clientNameIndex is the unique index on (user_id, lower(name)) from migration 00003.
const clientNameIndex = "clients_user_id_lower_name_idx"

// ClientStore implements the clients use case's storage (S2-07). Every statement is scoped to the
// owning user in SQL, so another user's client id matches no row (GO-47, SEC-12).
type ClientStore struct{ database Beginner }

// NewClientStore accepts a pool or lazy transaction factory from the composition root.
func NewClientStore(database Beginner) *ClientStore { return &ClientStore{database: database} }

// List returns one page of the user's clients and the total count in one snapshot.
func (s *ClientStore) List(ctx context.Context, userID string, limit, offset int32) ([]domain.Client, int64, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return nil, 0, fmt.Errorf("list clients: %w", err)
	}
	var (
		rows  []loghoursqueries.Client
		total int64
	)
	err = s.read(ctx, func(q *loghoursqueries.Queries) error {
		var err error
		if total, err = q.CountClients(ctx, owner); err != nil {
			return fmt.Errorf("count clients: %w", err)
		}
		if rows, err = q.ListClientsPage(ctx, loghoursqueries.ListClientsPageParams{UserID: owner, PageLimit: limit, PageOffset: offset}); err != nil {
			return fmt.Errorf("list clients: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]domain.Client, 0, len(rows))
	for _, row := range rows {
		items = append(items, toClient(row))
	}
	return items, total, nil
}

// Create saves a client. A case-insensitive duplicate returns *domain.ClientNameTakenError.
func (s *ClientStore) Create(ctx context.Context, userID, name string) (domain.Client, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Client{}, fmt.Errorf("create client: %w", err)
	}
	var created domain.Client
	err = s.write(ctx, func(q *loghoursqueries.Queries) error {
		if existing, err := q.GetClientByName(ctx, loghoursqueries.GetClientByNameParams{UserID: owner, Name: name}); err == nil {
			return &domain.ClientNameTakenError{Existing: toClient(existing)}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up client name: %w", err)
		}
		row, err := q.InsertClient(ctx, loghoursqueries.InsertClientParams{UserID: owner, Name: name})
		if err != nil {
			return fmt.Errorf("insert client: %w", err)
		}
		created = toClient(row)
		return nil
	})
	if isClientNameViolation(err) {
		// Two requests raced past the lookup; the loser reports the winner's client.
		return domain.Client{}, s.nameTaken(ctx, owner, name)
	}
	return created, err
}

// Rename changes a client's name. Another client's name returns *domain.ClientNameTakenError.
func (s *ClientStore) Rename(ctx context.Context, userID, clientID, name string) (domain.Client, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Client{}, fmt.Errorf("rename client: %w", err)
	}
	id, err := parseUUID(clientID)
	if err != nil {
		return domain.Client{}, domain.ErrNotFound
	}
	var renamed domain.Client
	err = s.write(ctx, func(q *loghoursqueries.Queries) error {
		if _, err := q.LockClient(ctx, loghoursqueries.LockClientParams{ID: id, UserID: owner}); err != nil {
			return notFoundOr(err, "lock client")
		}
		if existing, err := q.GetClientByName(ctx, loghoursqueries.GetClientByNameParams{UserID: owner, Name: name}); err == nil {
			if existing.ID != id {
				return &domain.ClientNameTakenError{Existing: toClient(existing)}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up client name: %w", err)
		}
		row, err := q.RenameClient(ctx, loghoursqueries.RenameClientParams{ID: id, UserID: owner, Name: name})
		if err != nil {
			return notFoundOr(err, "rename client")
		}
		renamed = toClient(row)
		return nil
	})
	if isClientNameViolation(err) {
		return domain.Client{}, s.nameTaken(ctx, owner, name)
	}
	return renamed, err
}

// Delete removes a client in one transaction. The client row is locked first, so no entry can
// start using it between the count and the delete. Its entry_clients rows go with it.
func (s *ClientStore) Delete(ctx context.Context, userID, clientID string, detach bool) error {
	owner, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("delete client: %w", err)
	}
	id, err := parseUUID(clientID)
	if err != nil {
		return domain.ErrNotFound
	}
	return s.write(ctx, func(q *loghoursqueries.Queries) error {
		if _, err := q.LockClient(ctx, loghoursqueries.LockClientParams{ID: id, UserID: owner}); err != nil {
			return notFoundOr(err, "lock client")
		}
		if !detach {
			count, err := q.CountClientEntries(ctx, loghoursqueries.CountClientEntriesParams{ClientID: id, UserID: owner})
			if err != nil {
				return fmt.Errorf("count client entries: %w", err)
			}
			if count > 0 {
				return &domain.ClientInUseError{EntryCount: int32(min(count, int64(1<<31-1)))} //nolint:gosec // G115: clamped above.
			}
		}
		deleted, err := q.DeleteClient(ctx, loghoursqueries.DeleteClientParams{ID: id, UserID: owner})
		if err != nil {
			return fmt.Errorf("delete client: %w", err)
		}
		if deleted != 1 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// nameTaken looks up the client that holds name after a unique-index violation. It returns a
// conflict even if the winner has already gone, so the caller can retry.
func (s *ClientStore) nameTaken(ctx context.Context, owner pgtype.UUID, name string) error {
	var existing loghoursqueries.Client
	err := s.read(ctx, func(q *loghoursqueries.Queries) error {
		var err error
		existing, err = q.GetClientByName(ctx, loghoursqueries.GetClientByNameParams{UserID: owner, Name: name})
		return err //nolint:wrapcheck // Wrapped by the caller below.
	})
	if err != nil {
		return fmt.Errorf("client name conflict: %w", errors.Join(domain.ErrConflict, err))
	}
	return &domain.ClientNameTakenError{Existing: toClient(existing)}
}

// write runs fn in a transaction that commits when fn returns nil.
func (s *ClientStore) write(ctx context.Context, fn func(*loghoursqueries.Queries) error) error {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin client transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(loghoursqueries.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit client transaction: %w", err)
	}
	return nil
}

// read runs fn in a transaction that is always rolled back (it only reads).
func (s *ClientStore) read(ctx context.Context, fn func(*loghoursqueries.Queries) error) error {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin client transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	return fn(loghoursqueries.New(tx))
}

func toClient(row loghoursqueries.Client) domain.Client {
	return domain.Client{ID: row.ID.String(), Name: row.Name}
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, errors.New("invalid identifier")
	}
	return id, nil
}

func notFoundOr(err error, action string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return fmt.Errorf("%s: %w", action, err)
}

func isClientNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == clientNameIndex
}
