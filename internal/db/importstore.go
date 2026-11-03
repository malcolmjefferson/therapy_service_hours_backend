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
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

// ImportStore runs the Markdown import (S4-03) with the static queries of db/queries/loghours/import.sql
// and the entry and client queries it shares with the endpoints.
type ImportStore struct{ database Beginner }

var _ importexport.Store = (*ImportStore)(nil)

// NewImportStore accepts a pool or lazy transaction factory from the composition root.
func NewImportStore(database Beginner) *ImportStore { return &ImportStore{database: database} }

// Import runs fn in one transaction for userID. The first statement takes a transaction-level
// advisory lock on the user, so that user's imports run one at a time and a second import sees the
// first one's entries; other users are not held up. Every error from fn rolls the transaction back.
func (s *ImportStore) Import(ctx context.Context, userID string, fn func(ctx context.Context, tx importexport.Tx) error) error {
	owner, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := lq.New(tx)
	if err := q.LockUserImport(ctx, userID); err != nil {
		return fmt.Errorf("lock user imports: %w", err)
	}
	if err := fn(ctx, &importTx{q: q, owner: owner}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit import transaction: %w", err)
	}
	return nil
}

// importTx is the storage of one import. Every statement carries the owner's id (GO-47).
type importTx struct {
	q     *lq.Queries
	owner pgtype.UUID
}

var _ importexport.Tx = (*importTx)(nil)

func (t *importTx) Categories(ctx context.Context) ([]domain.Category, error) {
	rows, err := t.q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	out := make([]domain.Category, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Category{ID: row.ID.String(), Name: row.Name, Group: domain.CategoryGroup(row.CategoryGroup)})
	}
	return out, nil
}

// MergeClient saves the name unless the user has it already, ignoring case. The insert does nothing
// on a clash instead of failing, because a failed statement would abort the whole transaction.
func (t *importTx) MergeClient(ctx context.Context, name string) (importexport.MergedClient, error) {
	row, err := t.q.InsertClientIfAbsent(ctx, lq.InsertClientIfAbsentParams{UserID: t.owner, Name: name})
	if err == nil {
		return importexport.MergedClient{Client: toClient(row), Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return importexport.MergedClient{}, fmt.Errorf("insert client: %w", err)
	}
	existing, err := t.q.GetClientByName(ctx, lq.GetClientByNameParams{UserID: t.owner, Name: name})
	if err != nil {
		return importexport.MergedClient{}, fmt.Errorf("look up client: %w", err)
	}
	return importexport.MergedClient{Client: toClient(existing)}, nil
}

func (t *importTx) EntriesBetween(ctx context.Context, first, last time.Time) ([]importexport.ExistingEntry, error) {
	rows, err := t.q.ListEntriesForImport(ctx, lq.ListEntriesForImportParams{
		UserID:   t.owner,
		FromDate: pgtype.Date{Time: first, Valid: true},
		ToDate:   pgtype.Date{Time: last, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	out := make([]importexport.ExistingEntry, 0, len(rows))
	for _, row := range rows {
		e := importexport.ExistingEntry{
			SessionDate:     row.SessionDate.Time,
			HasTimes:        row.StartTime.Valid && row.EndTime.Valid,
			DurationMinutes: row.DurationMinutes,
			CategoryID:      row.CategoryID.String(),
			Notes:           row.Notes.String,
			Status:          domain.EntryStatus(row.Status),
			ClientIDs:       make([]string, 0, len(row.ClientIds)),
		}
		if e.HasTimes {
			e.StartMinute, e.EndMinute = minuteOf(row.StartTime), minuteOf(row.EndTime)
		}
		for _, id := range row.ClientIds {
			e.ClientIDs = append(e.ClientIDs, id.String())
		}
		out = append(out, e)
	}
	return out, nil
}

// CreateEntry inserts the entry and its client links. An overlap can only come from a request that
// committed after EntriesBetween read the day; it fails the import with a conflict, which the caller
// may retry (409 in api/openapi.yaml).
func (t *importTx) CreateEntry(ctx context.Context, e importexport.NewEntry) error {
	category, err := parseUUID(e.CategoryID)
	if err != nil {
		return fmt.Errorf("category id: %w", err)
	}
	clients := make([]pgtype.UUID, 0, len(e.ClientIDs))
	for _, raw := range e.ClientIDs {
		id, err := parseUUID(raw)
		if err != nil {
			return fmt.Errorf("client id: %w", err)
		}
		clients = append(clients, id)
	}
	params := lq.InsertEntryParams{
		UserID: t.owner, SessionDate: pgtype.Date{Time: e.SessionDate, Valid: true},
		DurationMinutes: e.DurationMinutes, CategoryID: category, OnSite: e.OnSite,
		Notes: pgtype.Text{String: e.Notes, Valid: e.Notes != ""},
	}
	if e.HasTimes {
		params.StartTime, params.EndTime = minuteOfDay(e.StartMinute), minuteOfDay(e.EndMinute)
	}
	row, err := t.q.InsertEntry(ctx, params)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgOverlap && pgErr.ConstraintName == entryOverlapConstraint {
			return fmt.Errorf("insert entry: %w", errors.Join(domain.ErrConflict, err))
		}
		return fmt.Errorf("insert entry: %w", err)
	}
	return attachClients(ctx, t.q, t.owner, row.ID, clients)
}

// minuteOf is the minutes after midnight of a time column.
func minuteOf(t pgtype.Time) int32 {
	return int32(t.Microseconds / int64(time.Minute/time.Microsecond)) //nolint:gosec // G115: a time of day is under 1,440 minutes.
}
