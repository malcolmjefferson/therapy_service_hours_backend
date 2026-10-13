package loghoursqueries

import (
	"context"
	"errors"
)

// ErrInvalidSort means the sort key or direction is not on the allowlist. The HTTP layer answers 400
// (SEC-24); the SQL is never built from the value.
var ErrInvalidSort = errors.New("invalid sort")

// EntrySort is a sortable column of the entries table (FR-LOG-08).
type EntrySort string

// The sortable columns. Adding one means two generated queries, one entry in entryLists and a case in
// the all-sorts test (GO-14).
const (
	SortDate     EntrySort = "date"
	SortCategory EntrySort = "category"
	SortClient   EntrySort = "client"
	SortHours    EntrySort = "hours"
	SortOnSite   EntrySort = "on_site"
)

// SortDirection is ascending or descending.
type SortDirection string

// The two directions.
const (
	Asc  SortDirection = "asc"
	Desc SortDirection = "desc"
)

// ListEntriesParams are the optional filters (NULL means "no filter") and the page window, shared by
// every fixed list query and by CountEntries. Use PageOffset 0 and a positive PageSize.
type ListEntriesParams = ListEntriesDateDescParams

type sortKey struct {
	sort EntrySort
	dir  SortDirection
}

type listFunc func(*Queries, context.Context, ListEntriesParams) ([]Entry, error)

// entryLists is the sort allowlist (SEC-24, GO-14): each permitted pair maps to one fixed query.
var entryLists = map[sortKey]listFunc{
	{SortDate, Asc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesDateAsc(ctx, ListEntriesDateAscParams(p))
	},
	{SortDate, Desc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesDateDesc(ctx, ListEntriesDateDescParams(p))
	},
	{SortCategory, Asc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesCategoryAsc(ctx, ListEntriesCategoryAscParams(p))
	},
	{SortCategory, Desc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesCategoryDesc(ctx, ListEntriesCategoryDescParams(p))
	},
	{SortClient, Asc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesClientAsc(ctx, ListEntriesClientAscParams(p))
	},
	{SortClient, Desc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesClientDesc(ctx, ListEntriesClientDescParams(p))
	},
	{SortHours, Asc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesHoursAsc(ctx, ListEntriesHoursAscParams(p))
	},
	{SortHours, Desc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesHoursDesc(ctx, ListEntriesHoursDescParams(p))
	},
	{SortOnSite, Asc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesOnSiteAsc(ctx, ListEntriesOnSiteAscParams(p))
	},
	{SortOnSite, Desc}: func(q *Queries, ctx context.Context, p ListEntriesParams) ([]Entry, error) {
		return q.ListEntriesOnSiteDesc(ctx, ListEntriesOnSiteDescParams(p))
	},
}

// ListEntries returns one page of the user's entries in the requested order. A sort or direction that
// is not on the allowlist returns ErrInvalidSort before any query runs. The default order of the
// entries table is SortDate, Desc (newest first).
func (q *Queries) ListEntries(ctx context.Context, sort EntrySort, dir SortDirection, p ListEntriesParams) ([]Entry, error) {
	list, ok := entryLists[sortKey{sort, dir}]
	if !ok {
		return nil, ErrInvalidSort
	}
	return list(q, ctx, p)
}
