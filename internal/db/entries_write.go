package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	lq "github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// Constraint names from migration 00003_log_hours.sql. PostgreSQL names the foreign keys by its default rule.
const (
	entryOverlapConstraint  = "entries_no_overlap_excl"
	entryCategoryConstraint = "entries_category_id_fkey"
	entryClientConstraint   = "entry_clients_client_id_user_id_fkey"
)

// pgOverlap and pgForeignKey are the SQLSTATE codes of an exclusion and a foreign-key violation.
const (
	pgOverlap    = "23P01"
	pgForeignKey = "23503"
)

// GetEntry returns one of the user's entries, or domain.ErrNotFound when it is missing or not theirs.
func (s *EntryStore) GetEntry(ctx context.Context, userID, entryID string) (domain.Entry, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("get entry: %w", err)
	}
	id, err := parseUUID(entryID)
	if err != nil {
		return domain.Entry{}, domain.ErrNotFound
	}
	var out domain.Entry
	err = s.inTx(ctx, false, func(q *lq.Queries) error {
		row, err := q.GetEntry(ctx, lq.GetEntryParams{ID: id, UserID: owner})
		if err != nil {
			return notFoundOr(err, "get entry")
		}
		out, err = loadEntry(ctx, q, owner, row)
		return err
	})
	return out, err
}

// CreateEntry inserts the entry and its client links in one transaction. The database decides
// overlaps through entries_no_overlap_excl, so two concurrent overlapping creates cannot both win.
func (s *EntryStore) CreateEntry(ctx context.Context, userID string, v entries.Validated) (domain.Entry, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("create entry: %w", err)
	}
	fields, err := entryFields(v)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("create entry: %w", err)
	}
	var out domain.Entry
	err = s.inTx(ctx, true, func(q *lq.Queries) error {
		row, err := q.InsertEntry(ctx, lq.InsertEntryParams{
			UserID: owner, SessionDate: fields.date, StartTime: fields.start, EndTime: fields.end,
			DurationMinutes: v.DurationMinutes, CategoryID: fields.category, OnSite: v.OnSite, Notes: fields.notes,
		})
		if err != nil {
			return fmt.Errorf("insert entry: %w", err)
		}
		if err := attachClients(ctx, q, owner, row.ID, fields.clients); err != nil {
			return err
		}
		out, err = loadEntry(ctx, q, owner, row)
		return err
	})
	if err != nil {
		return domain.Entry{}, s.writeError(ctx, err, owner, fields, pgtype.UUID{})
	}
	return out, nil
}

// UpdateEntry replaces the entry's editable fields and client set in one transaction. The UPDATE
// only matches the owner's pending or rejected entries, so an approval that lands first wins.
func (s *EntryStore) UpdateEntry(ctx context.Context, userID, entryID string, v entries.Validated) (domain.Entry, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("update entry: %w", err)
	}
	id, err := parseUUID(entryID)
	if err != nil {
		return domain.Entry{}, domain.ErrNotFound
	}
	fields, err := entryFields(v)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("update entry: %w", err)
	}
	var out domain.Entry
	err = s.inTx(ctx, true, func(q *lq.Queries) error {
		row, err := q.UpdateEntry(ctx, lq.UpdateEntryParams{
			ID: id, UserID: owner, SessionDate: fields.date, StartTime: fields.start, EndTime: fields.end,
			DurationMinutes: v.DurationMinutes, CategoryID: fields.category, OnSite: v.OnSite, Notes: fields.notes,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return lockedOrMissing(ctx, q, id, owner)
		}
		if err != nil {
			return fmt.Errorf("update entry: %w", err)
		}
		if err := q.DeleteEntryClients(ctx, lq.DeleteEntryClientsParams{EntryID: id, UserID: owner}); err != nil {
			return fmt.Errorf("detach entry clients: %w", err)
		}
		if err := attachClients(ctx, q, owner, id, fields.clients); err != nil {
			return err
		}
		out, err = loadEntry(ctx, q, owner, row)
		return err
	})
	if err != nil {
		return domain.Entry{}, s.writeError(ctx, err, owner, fields, id)
	}
	return out, nil
}

// DeleteEntry removes a pending or rejected entry; its client links go with it (ON DELETE CASCADE).
func (s *EntryStore) DeleteEntry(ctx context.Context, userID, entryID string) error {
	owner, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	id, err := parseUUID(entryID)
	if err != nil {
		return domain.ErrNotFound
	}
	return s.inTx(ctx, true, func(q *lq.Queries) error {
		deleted, err := q.DeleteEntry(ctx, lq.DeleteEntryParams{ID: id, UserID: owner})
		if err != nil {
			return fmt.Errorf("delete entry: %w", err)
		}
		if deleted == 0 {
			return lockedOrMissing(ctx, q, id, owner)
		}
		return nil
	})
}

// storedFields are a validated entry converted to the column types the queries take.
type storedFields struct {
	date       pgtype.Date
	start, end pgtype.Time
	category   pgtype.UUID
	clients    []pgtype.UUID
	notes      pgtype.Text
}

func entryFields(v entries.Validated) (storedFields, error) {
	category, err := parseUUID(v.CategoryID)
	if err != nil {
		return storedFields{}, fmt.Errorf("category id: %w", err)
	}
	clients := make([]pgtype.UUID, 0, len(v.ClientIDs))
	for _, raw := range v.ClientIDs {
		id, err := parseUUID(raw)
		if err != nil {
			return storedFields{}, fmt.Errorf("client id: %w", err)
		}
		clients = append(clients, id)
	}
	return storedFields{
		date:     pgtype.Date{Time: v.SessionDate, Valid: true},
		start:    minuteOfDay(v.StartMinute),
		end:      minuteOfDay(v.EndMinute),
		category: category,
		clients:  clients,
		notes:    pgtype.Text{String: v.Notes, Valid: v.Notes != ""},
	}, nil
}

func minuteOfDay(minute int32) pgtype.Time {
	return pgtype.Time{Microseconds: int64(minute) * int64(time.Minute/time.Microsecond), Valid: true}
}

func attachClients(ctx context.Context, q *lq.Queries, owner, entry pgtype.UUID, clients []pgtype.UUID) error {
	if len(clients) == 0 {
		return nil
	}
	if err := q.InsertEntryClients(ctx, lq.InsertEntryClientsParams{EntryID: entry, UserID: owner, ClientIds: clients}); err != nil {
		return fmt.Errorf("attach entry clients: %w", err)
	}
	return nil
}

// lockedOrMissing explains why a conditional UPDATE or DELETE matched no row: the entry is the
// user's own and approved (locked), or it is absent or someone else's (not found, never saying which).
func lockedOrMissing(ctx context.Context, q *lq.Queries, id, owner pgtype.UUID) error {
	if _, err := q.GetEntry(ctx, lq.GetEntryParams{ID: id, UserID: owner}); err != nil {
		return notFoundOr(err, "look up entry")
	}
	return &domain.EntryLockedError{}
}

// loadEntry returns the stored row with its category and clients as the owner sees them.
func loadEntry(ctx context.Context, q *lq.Queries, owner pgtype.UUID, row lq.Entry) (domain.Entry, error) {
	categories, err := categoriesByID(ctx, q)
	if err != nil {
		return domain.Entry{}, err
	}
	category, ok := categories[row.CategoryID]
	if !ok {
		return domain.Entry{}, errors.New("entry has an unknown category")
	}
	clients, err := clientsByEntry(ctx, q, owner, []lq.Entry{row})
	if err != nil {
		return domain.Entry{}, err
	}
	return entryFromRow(row, category, clients[row.ID])
}

// writeError turns the database errors of a create or update into domain errors. The failed
// transaction is already rolled back, so the clash is looked up in a new snapshot; if the winner
// has gone by then the caller gets a plain conflict and can retry.
func (s *EntryStore) writeError(ctx context.Context, err error, owner pgtype.UUID, f storedFields, exclude pgtype.UUID) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == pgOverlap && pgErr.ConstraintName == entryOverlapConstraint:
		return s.overlap(ctx, owner, f, exclude)
	case pgErr.Code == pgForeignKey && pgErr.ConstraintName == entryCategoryConstraint:
		return domain.NewValidationError(domain.FieldError{Field: "category_id", Message: "must be an existing category"})
	case pgErr.Code == pgForeignKey && pgErr.ConstraintName == entryClientConstraint:
		return domain.NewValidationError(domain.FieldError{Field: "client_ids", Message: "must contain only your own saved clients"})
	default:
		return err
	}
}

func (s *EntryStore) overlap(ctx context.Context, owner pgtype.UUID, f storedFields, exclude pgtype.UUID) error {
	var clash lq.FindOverlappingEntryRow
	err := s.inTx(ctx, false, func(q *lq.Queries) error {
		var err error
		clash, err = q.FindOverlappingEntry(ctx, lq.FindOverlappingEntryParams{
			UserID: owner, SessionDate: f.date, StartTime: f.start, EndTime: f.end, ExcludeID: exclude,
		})
		return err //nolint:wrapcheck // Wrapped by the caller below.
	})
	if err != nil {
		return fmt.Errorf("entry overlap: %w", errors.Join(domain.ErrConflict, err))
	}
	start, err := clockText(clash.StartTime)
	if err != nil {
		return fmt.Errorf("entry overlap: %w", err)
	}
	end, err := clockText(clash.EndTime)
	if err != nil {
		return fmt.Errorf("entry overlap: %w", err)
	}
	return &domain.EntryOverlapError{Conflicting: domain.ConflictingEntry{
		ID: clash.ID.String(), SessionDate: clash.SessionDate.Time, StartTime: start, EndTime: end,
	}}
}

// inTx runs fn in a transaction. A writing transaction commits when fn succeeds; a read-only one is
// always rolled back.
func (s *EntryStore) inTx(ctx context.Context, commit bool, fn func(*lq.Queries) error) error {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin entry transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(lq.New(tx)); err != nil {
		return err
	}
	if !commit {
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit entry transaction: %w", err)
	}
	return nil
}
