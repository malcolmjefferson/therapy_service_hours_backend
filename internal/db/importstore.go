package db

import (
	"context"
	"crypto/rand"
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

// CreateEntries inserts the entries and their client links with two statements, however many rows
// there are: a per-row insert took one round trip each, which does not fit the API Lambda's timeout
// for a 5,000-row file (S4-04). An overlap can only come from a request that committed after
// EntriesBetween read the days; it fails the import with a conflict, which the caller may retry (409
// in api/openapi.yaml), and nothing is stored.
func (t *importTx) CreateEntries(ctx context.Context, entries []importexport.NewEntry) error {
	if len(entries) == 0 {
		return nil
	}
	n := len(entries)
	entryParams := lq.InsertImportedEntriesParams{
		UserID: t.owner, Ids: make([]pgtype.UUID, 0, n), SessionDates: make([]pgtype.Date, 0, n),
		StartTimes: make([]pgtype.Time, 0, n), EndTimes: make([]pgtype.Time, 0, n),
		DurationMinutes: make([]int32, 0, n), CategoryIds: make([]pgtype.UUID, 0, n),
		OnSites: make([]bool, 0, n), Notes: make([]string, 0, n),
	}
	linkParams := lq.InsertImportedEntryClientsParams{UserID: t.owner}
	for _, e := range entries {
		category, err := parseUUID(e.CategoryID)
		if err != nil {
			return fmt.Errorf("category id: %w", err)
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		var start, end pgtype.Time // Invalid (NULL) for an entry that needs a time.
		if e.HasTimes {
			start, end = minuteOfDay(e.StartMinute), minuteOfDay(e.EndMinute)
		}
		entryParams.Ids = append(entryParams.Ids, id)
		entryParams.SessionDates = append(entryParams.SessionDates, pgtype.Date{Time: e.SessionDate, Valid: true})
		entryParams.StartTimes = append(entryParams.StartTimes, start)
		entryParams.EndTimes = append(entryParams.EndTimes, end)
		entryParams.DurationMinutes = append(entryParams.DurationMinutes, e.DurationMinutes)
		entryParams.CategoryIds = append(entryParams.CategoryIds, category)
		entryParams.OnSites = append(entryParams.OnSites, e.OnSite)
		entryParams.Notes = append(entryParams.Notes, e.Notes)
		for _, raw := range e.ClientIDs {
			client, err := parseUUID(raw)
			if err != nil {
				return fmt.Errorf("client id: %w", err)
			}
			linkParams.EntryIds = append(linkParams.EntryIds, id)
			linkParams.ClientIds = append(linkParams.ClientIds, client)
		}
	}
	if err := t.q.InsertImportedEntries(ctx, entryParams); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgOverlap && pgErr.ConstraintName == entryOverlapConstraint {
			return fmt.Errorf("insert entries: %w", errors.Join(domain.ErrConflict, err))
		}
		return fmt.Errorf("insert entries: %w", err)
	}
	if len(linkParams.EntryIds) == 0 {
		return nil
	}
	if err := t.q.InsertImportedEntryClients(ctx, linkParams); err != nil {
		return fmt.Errorf("attach entry clients: %w", err)
	}
	return nil
}

// newUUID returns a random (version 4) UUID, so a whole file's entries can be named before they are
// stored and their client links can refer to them in the next statement.
func newUUID() (pgtype.UUID, error) {
	var id pgtype.UUID
	if _, err := rand.Read(id.Bytes[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("generate entry id: %w", err)
	}
	id.Bytes[6] = id.Bytes[6]&0x0f | 0x40 // Version 4.
	id.Bytes[8] = id.Bytes[8]&0x3f | 0x80 // RFC 4122 variant.
	id.Valid = true
	return id, nil
}

// minuteOf is the minutes after midnight of a time column.
func minuteOf(t pgtype.Time) int32 {
	return int32(t.Microseconds / int64(time.Minute/time.Microsecond)) //nolint:gosec // G115: a time of day is under 1,440 minutes.
}
