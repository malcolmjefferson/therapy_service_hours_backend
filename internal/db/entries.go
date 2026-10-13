package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	lq "github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// EntryStore reads a user's entries with the static S2-02 queries.
type EntryStore struct{ database Beginner }

var _ entries.Store = (*EntryStore)(nil)

// NewEntryStore accepts a pool or lazy transaction factory from the composition root.
func NewEntryStore(database Beginner) *EntryStore { return &EntryStore{database: database} }

// sorts maps an API sort column to the query allowlist; a column missing here fails closed (GO-14).
var sorts = map[domain.EntrySort]lq.EntrySort{
	domain.SortSessionDate: lq.SortDate,
	domain.SortCategory:    lq.SortCategory,
	domain.SortClient:      lq.SortClient,
	domain.SortHours:       lq.SortHours,
	domain.SortOnSite:      lq.SortOnSite,
}

var directions = map[domain.SortDirection]lq.SortDirection{
	domain.SortAsc:  lq.Asc,
	domain.SortDesc: lq.Desc,
}

// ListEntries reads the page, its total, the page's clients and the category names in one read-only
// snapshot, so the total always agrees with the rows. Every query is scoped to userID in its SQL.
func (s *EntryStore) ListEntries(ctx context.Context, userID string, q entries.ListQuery) ([]domain.Entry, int64, error) {
	sortColumn, okSort := sorts[q.Sort]
	direction, okDir := directions[q.Dir]
	if !okSort || !okDir {
		return nil, 0, lq.ErrInvalidSort
	}
	var owner pgtype.UUID
	if err := owner.Scan(userID); err != nil {
		return nil, 0, fmt.Errorf("parse user ID: %w", err)
	}
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("begin entries read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Must be the first statement of the transaction.
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return nil, 0, fmt.Errorf("set snapshot: %w", err)
	}
	queries := lq.New(tx)
	params := listParams(owner, q)

	rows, err := queries.ListEntries(ctx, sortColumn, direction, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list entries: %w", err)
	}
	total, err := queries.CountEntries(ctx, lq.CountEntriesParams{
		UserID: params.UserID, CategoryGroup: params.CategoryGroup, FromDate: params.FromDate,
		ToDate: params.ToDate, ClientText: params.ClientText,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count entries: %w", err)
	}
	if len(rows) == 0 {
		return []domain.Entry{}, total, nil
	}
	categories, err := categoriesByID(ctx, queries)
	if err != nil {
		return nil, 0, err
	}
	clients, err := clientsByEntry(ctx, queries, owner, rows)
	if err != nil {
		return nil, 0, err
	}
	out := make([]domain.Entry, 0, len(rows))
	for _, row := range rows {
		category, ok := categories[row.CategoryID]
		if !ok {
			return nil, 0, errors.New("entry has an unknown category")
		}
		entry, err := entryFromRow(row, category, clients[row.ID])
		if err != nil {
			return nil, 0, err
		}
		out = append(out, entry)
	}
	return out, total, nil
}

func listParams(owner pgtype.UUID, q entries.ListQuery) lq.ListEntriesParams {
	p := lq.ListEntriesParams{UserID: owner, PageOffset: int32(q.Offset()), PageSize: int32(q.PageSize)} //nolint:gosec // G115: Page and PageSize are bounded by the service (1..100000 and 1..200).
	if q.Group != nil {
		p.CategoryGroup = pgtype.Text{String: string(*q.Group), Valid: true}
	}
	if q.Client != nil {
		p.ClientText = pgtype.Text{String: *q.Client, Valid: true}
	}
	if q.From != nil {
		p.FromDate = pgtype.Date{Time: *q.From, Valid: true}
	}
	if q.To != nil {
		p.ToDate = pgtype.Date{Time: *q.To, Valid: true}
	}
	return p
}

func categoriesByID(ctx context.Context, q *lq.Queries) (map[pgtype.UUID]domain.CategoryRef, error) {
	rows, err := q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	out := make(map[pgtype.UUID]domain.CategoryRef, len(rows))
	for _, c := range rows {
		out[c.ID] = domain.CategoryRef{ID: c.ID.String(), Name: c.Name, Group: domain.CategoryGroup(c.CategoryGroup)}
	}
	return out, nil
}

func clientsByEntry(ctx context.Context, q *lq.Queries, owner pgtype.UUID, page []lq.Entry) (map[pgtype.UUID][]domain.ClientRef, error) {
	ids := make([]pgtype.UUID, len(page))
	for i, e := range page {
		ids[i] = e.ID
	}
	rows, err := q.ListEntryClients(ctx, lq.ListEntryClientsParams{UserID: owner, EntryIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list entry clients: %w", err)
	}
	out := make(map[pgtype.UUID][]domain.ClientRef, len(page))
	for _, c := range rows {
		out[c.EntryID] = append(out[c.EntryID], domain.ClientRef{ID: c.ClientID.String(), Name: c.Name})
	}
	return out, nil
}

func entryFromRow(row lq.Entry, category domain.CategoryRef, clients []domain.ClientRef) (domain.Entry, error) {
	start, err := clockText(row.StartTime)
	if err != nil {
		return domain.Entry{}, err
	}
	end, err := clockText(row.EndTime)
	if err != nil {
		return domain.Entry{}, err
	}
	e := domain.Entry{
		ID: row.ID.String(), SessionDate: row.SessionDate.Time, StartTime: start, EndTime: end,
		DurationMinutes: int(row.DurationMinutes), Category: category, Clients: clients, OnSite: row.OnSite,
		Notes: row.Notes.String, Status: domain.EntryStatus(row.Status), NeedsTime: row.NeedsTime,
		ReviewNote: row.ReviewNote.String, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if e.Clients == nil {
		e.Clients = []domain.ClientRef{}
	}
	if row.ReviewedAt.Valid {
		at := row.ReviewedAt.Time
		e.ReviewedAt = &at
	}
	return e, nil
}

// microsecondsPerDay is 24:00, a value the time column accepts but the API contract does not (its
// times are 00:00 to 23:59, and a session never crosses midnight).
const microsecondsPerDay = int64(24 * time.Hour / time.Microsecond)

// clockText formats a PostgreSQL time as "HH:MM", or "" when it is NULL. A stored 24:00 cannot be
// written as a contract time, so it fails closed instead of being shown as 00:00.
func clockText(t pgtype.Time) (string, error) {
	if !t.Valid {
		return "", nil
	}
	if t.Microseconds < 0 || t.Microseconds >= microsecondsPerDay {
		return "", errors.New("stored session time is outside 00:00 to 23:59")
	}
	return time.Time{}.Add(time.Duration(t.Microseconds) * time.Microsecond).Format("15:04"), nil
}
