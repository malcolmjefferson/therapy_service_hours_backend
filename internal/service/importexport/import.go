package importexport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// The reasons the import service adds to the parser's (see SkipReason in api/openapi.yaml).
const (
	// ReasonDuplicate means an existing entry has the same date, category, hours, notes and clients (ASM-07).
	ReasonDuplicate SkipReason = "duplicate"
	// ReasonOverlap means the row has times that overlap another non-rejected session of the user.
	ReasonOverlap SkipReason = "overlap"
	// ReasonUnknownCategory means the category is not one of the service categories. It has the value
	// of the parser's ReasonUnknownGroup, which reports an unreadable group or category cell with the
	// same reason.
	ReasonUnknownCategory = ReasonUnknownGroup
)

// Messages of the skip reasons only the import service gives. They never quote the cell.
const (
	messageDuplicate       = "Same date, category, hours, notes and clients as an existing entry."
	messageOverlap         = "The session overlaps another of your sessions."
	messageUnknownCategory = "The category is not one of the service categories."
)

// MergedClient is one client name found in the file and what the import did with it.
type MergedClient struct {
	Client domain.Client
	// Created is true when the import saved a new client, false when the user already had one with that name.
	Created bool
}

// SkippedRow is a row that was not imported. Message never quotes the cell.
type SkippedRow struct {
	// Row is the 1-based position in the entries table, not counting the header rows.
	Row     int
	Reason  SkipReason
	Field   string
	Message string
}

// Report is what an import did (ImportReport in api/openapi.yaml).
type Report struct {
	// TotalRows is the number of rows in the entries table: ImportedCount + len(SkippedRows).
	TotalRows     int
	ImportedCount int
	// NeedsTimeCount is how many of the imported entries have no times and are flagged Needs time.
	NeedsTimeCount int
	// MergedClients lists every distinct client name in the file, ordered by name ignoring case, then id.
	MergedClients []MergedClient
	// SkippedRows are the rows that were not imported, in file order.
	SkippedRows []SkippedRow
}

// NewEntry is an entry to insert. It has been checked by Parse; the database repeats every rule.
type NewEntry struct {
	// SessionDate is midnight UTC on the session date; only the date part is meaningful.
	SessionDate time.Time
	// HasTimes is false for a legacy row, which imports as Needs time with only its duration.
	HasTimes bool
	// StartMinute and EndMinute are minutes after midnight; they are meaningful only with HasTimes.
	StartMinute, EndMinute int32
	DurationMinutes        int32
	CategoryID             string
	// ClientIDs are the user's saved clients, without duplicates.
	ClientIDs []string
	OnSite    bool
	// Notes is trimmed; empty means none.
	Notes string
}

// ExistingEntry is one of the user's stored entries, as far as the import compares it with a row.
type ExistingEntry struct {
	SessionDate time.Time
	// HasTimes is false for an entry that needs a time; StartMinute and EndMinute are then zero.
	HasTimes               bool
	StartMinute, EndMinute int32
	DurationMinutes        int32
	CategoryID             string
	Notes                  string
	Status                 domain.EntryStatus
	ClientIDs              []string
}

// Tx is the storage an import uses inside its one transaction. Every method is scoped to the user
// the transaction was opened for.
type Tx interface {
	// Categories returns the service categories.
	Categories(ctx context.Context) ([]domain.Category, error)
	// MergeClient returns the user's client with that name, ignoring case, and saves one first when
	// there is none. It reports whether it saved a new client.
	MergeClient(ctx context.Context, name string) (MergedClient, error)
	// EntriesBetween returns the user's entries with a session date from first to last, inclusive,
	// in every status, with the ids of their clients.
	EntriesBetween(ctx context.Context, first, last time.Time) ([]ExistingEntry, error)
	// CreateEntry stores a pending entry with its clients. A session that overlaps another
	// non-rejected one, which only a concurrent request can cause after the service checked,
	// returns an error that matches domain.ErrConflict.
	CreateEntry(ctx context.Context, entry NewEntry) error
}

// Store runs an import's storage work in one transaction.
type Store interface {
	// Import runs fn in a transaction scoped to userID and commits it when fn returns nil; any error
	// rolls everything back. Imports by the same user run one after the other, so the duplicates a
	// second import looks for include everything the first one stored.
	Import(ctx context.Context, userID string, fn func(ctx context.Context, tx Tx) error) error
}

// Service imports a parsed ledger as the actor's own data.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("import service requires a store")
	}
	return &Service{store: store}, nil
}

// Import parses a ledger in either layout and stores it as the actor's own entries and clients, all
// in one transaction (FR-IO-02, FR-IO-03):
//
//   - A row without times becomes an entry that needs a time, with its length from Hours.
//   - Client names are merged into the actor's saved clients, ignoring case.
//   - A row whose date, category, hours, notes and clients match an existing entry is skipped as a
//     duplicate (ASM-07). Matching is one to one: a file with the same session twice imports both
//     the first time, and skips both when imported again.
//   - A row with times that overlaps another non-rejected session, stored or earlier in the file,
//     is skipped as an overlap. Rows without times never overlap.
//   - A bad row is reported and the valid rows are kept. Only a file that cannot be read at all, or
//     a storage failure, fails the import, and then nothing is stored.
//
// The errors of Parse (ErrTooLarge, ErrTooManyRows, ErrNotText, ErrNoEntriesTable) are returned for
// a file that is refused whole; check them with errors.Is.
func (s *Service) Import(ctx context.Context, actor domain.Actor, r io.Reader) (Report, error) {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return Report{}, domain.ErrForbidden
	}
	ledger, err := Parse(r)
	if err != nil {
		return Report{}, err
	}
	var report Report
	err = s.store.Import(ctx, actor.ID, func(ctx context.Context, tx Tx) error {
		var err error
		report, err = run(ctx, tx, ledger)
		return err
	})
	if err != nil {
		return Report{}, fmt.Errorf("import ledger: %w", err)
	}
	return report, nil
}

// run does the work of one import inside its transaction.
func run(ctx context.Context, tx Tx, ledger Ledger) (Report, error) {
	categories, err := tx.Categories(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list categories: %w", err)
	}
	merged, err := mergeClients(ctx, tx, ledger.ClientNames())
	if err != nil {
		return Report{}, err
	}
	p := &planner{categories: indexCategories(categories), clients: merged.byName}
	if len(ledger.Rows) > 0 {
		first, last := dateRange(ledger.Rows)
		stored, err := tx.EntriesBetween(ctx, first, last)
		if err != nil {
			return Report{}, fmt.Errorf("list existing entries: %w", err)
		}
		p.load(stored)
	}

	report := Report{TotalRows: ledger.TotalRows, MergedClients: merged.list}
	for _, bad := range ledger.Skipped {
		report.SkippedRows = append(report.SkippedRows, SkippedRow{Row: bad.Number, Reason: bad.Reason, Field: bad.Field, Message: bad.Message})
	}
	for _, row := range ledger.Rows {
		entry, skip := p.accept(row)
		if skip != nil {
			report.SkippedRows = append(report.SkippedRows, *skip)
			continue
		}
		if err := tx.CreateEntry(ctx, entry); err != nil {
			return Report{}, fmt.Errorf("create entry: %w", err)
		}
		report.ImportedCount++
		if !entry.HasTimes {
			report.NeedsTimeCount++
		}
	}
	// The parser's skips and ours are interleaved in file order.
	sort.SliceStable(report.SkippedRows, func(i, j int) bool { return report.SkippedRows[i].Row < report.SkippedRows[j].Row })
	return report, nil
}

// mergedClients is the result of saving a file's client names.
type mergedClients struct {
	// list is ordered by name ignoring case, then id.
	list []MergedClient
	// byName maps a lower-case name to the client id.
	byName map[string]string
}

// mergeClients saves each name that the user does not have yet. Names go in alphabetical order, so
// two imports at once ask for the same rows in the same order.
func mergeClients(ctx context.Context, tx Tx, names []string) (mergedClients, error) {
	sorted := slices.Clone(names)
	slices.SortFunc(sorted, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	out := mergedClients{list: make([]MergedClient, 0, len(sorted)), byName: make(map[string]string, len(sorted))}
	for _, name := range sorted {
		m, err := tx.MergeClient(ctx, name)
		if err != nil {
			return mergedClients{}, fmt.Errorf("merge client: %w", err)
		}
		out.list = append(out.list, m)
		out.byName[strings.ToLower(name)] = m.Client.ID
	}
	slices.SortStableFunc(out.list, func(a, b MergedClient) int {
		if c := strings.Compare(strings.ToLower(a.Client.Name), strings.ToLower(b.Client.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Client.ID, b.Client.ID)
	})
	return out, nil
}

// dateRange returns the earliest and latest session date of rows, which are not empty.
func dateRange(rows []Row) (first, last time.Time) {
	first, last = rows[0].Date, rows[0].Date
	for _, row := range rows[1:] {
		if row.Date.Before(first) {
			first = row.Date
		}
		if row.Date.After(last) {
			last = row.Date
		}
	}
	return first, last
}

func indexCategories(categories []domain.Category) map[string]domain.Category {
	out := make(map[string]domain.Category, len(categories))
	for _, c := range categories {
		out[strings.ToLower(strings.TrimSpace(c.Name))] = c
	}
	return out
}

// sessionKey is what makes two entries the same session for ASM-07. Times are left out on purpose:
// a legacy entry keeps matching its row after the user has added a time to it.
type sessionKey struct {
	date     string
	category string
	minutes  int32
	notes    string
	clients  string // sorted ids joined by a comma
}

func keyFor(date time.Time, category string, minutes int32, notes string, clientIDs []string) sessionKey {
	ids := slices.Clone(clientIDs)
	slices.Sort(ids)
	return sessionKey{date: date.Format(time.DateOnly), category: category, minutes: minutes, notes: notes, clients: strings.Join(ids, ",")}
}

// span is a session's time on one day, in minutes after midnight, half-open like the database range.
type span struct{ start, end int32 }

func (a span) overlaps(b span) bool { return a.start < b.end && a.end > b.start }

// planner decides for each row whether to import it. It knows the stored entries and the rows it
// has accepted so far, and nothing else.
type planner struct {
	categories map[string]domain.Category
	clients    map[string]string
	// unmatched counts the stored entries that no row has matched yet, by session.
	unmatched map[sessionKey]int
	// busy holds the timed, non-rejected sessions of each day: stored ones and accepted rows.
	busy map[string][]span
}

// load reads the stored entries the rows are compared with.
func (p *planner) load(stored []ExistingEntry) {
	p.unmatched = make(map[sessionKey]int, len(stored))
	p.busy = make(map[string][]span)
	for _, e := range stored {
		p.unmatched[keyFor(e.SessionDate, e.CategoryID, e.DurationMinutes, e.Notes, e.ClientIDs)]++
		if e.HasTimes && e.Status != domain.StatusRejected {
			day := e.SessionDate.Format(time.DateOnly)
			p.busy[day] = append(p.busy[day], span{e.StartMinute, e.EndMinute})
		}
	}
}

// accept returns the entry to store for row, or why the row is skipped.
func (p *planner) accept(row Row) (NewEntry, *SkippedRow) {
	category, skip := p.category(row)
	if skip != nil {
		return NewEntry{}, skip
	}
	clientIDs := make([]string, 0, len(row.Clients))
	for _, name := range row.Clients {
		clientIDs = append(clientIDs, p.clients[strings.ToLower(name)])
	}
	minutes := int32(row.DurationMinutes) //nolint:gosec // G115: the parser keeps it within 1 to 1440.
	key := keyFor(row.Date, category.ID, minutes, row.Notes, clientIDs)
	if p.unmatched[key] > 0 {
		p.unmatched[key]--
		return NewEntry{}, &SkippedRow{Row: row.Number, Reason: ReasonDuplicate, Message: messageDuplicate}
	}
	entry := NewEntry{
		SessionDate: row.Date, DurationMinutes: minutes, CategoryID: category.ID,
		ClientIDs: clientIDs, OnSite: row.OnSite, Notes: row.Notes,
	}
	if row.HasTimes() {
		start, _ := clockMinutes(row.StartTime)
		end, _ := clockMinutes(row.EndTime)
		s := span{start, end}
		day := row.Date.Format(time.DateOnly)
		if slices.ContainsFunc(p.busy[day], s.overlaps) {
			return NewEntry{}, &SkippedRow{Row: row.Number, Reason: ReasonOverlap, Message: messageOverlap}
		}
		p.busy[day] = append(p.busy[day], s)
		entry.HasTimes, entry.StartMinute, entry.EndMinute = true, s.start, s.end
	}
	return entry, nil
}

// category matches the row's category by name, ignoring case. The group cell was checked by Parse;
// the category's group is the one in the lookup table, so a row that names another group still lands
// in the right one.
func (p *planner) category(row Row) (domain.Category, *SkippedRow) {
	c, ok := p.categories[strings.ToLower(strings.TrimSpace(row.Category))]
	if !ok {
		return domain.Category{}, &SkippedRow{Row: row.Number, Reason: ReasonUnknownCategory, Field: fieldCategory, Message: messageUnknownCategory}
	}
	return c, nil
}

// clockMinutes reads the "HH:MM" the parser produced into minutes after midnight.
func clockMinutes(s string) (int32, bool) {
	m, ok := parseClock(s)
	return int32(m), ok //nolint:gosec // G115: a clock time is at most 23*60+59.
}
