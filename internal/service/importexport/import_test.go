package importexport_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

// Unit tests of the import service over an in-memory store. The PostgreSQL behavior (one transaction,
// constraints, concurrency) is covered by import_store_test.go. Names and notes are fictional
// pseudonyms (CLAUDE.md §0.7).

// fakeStore is an in-memory importexport.Store. Import works on a copy of the state and keeps it
// only when fn succeeds, like a transaction.
type fakeStore struct {
	categories []domain.Category
	clients    []domain.Client
	entries    []importexport.NewEntry
	// stored are entries the user already has, as the import reads them back.
	stored []importexport.ExistingEntry

	failOn  string // "categories", "merge", "between" or "create"
	opened  int
	nextID  int
	failErr error
}

var testCategories = []domain.Category{
	{ID: "c1", Name: "Individual Counseling", Group: domain.GroupDirect},
	{ID: "c2", Name: "Group Facilitation", Group: domain.GroupDirect},
	{ID: "c3", Name: "Writing Progress Notes / Documentation", Group: domain.GroupIndirect},
	{ID: "c4", Name: "Staff Meetings", Group: domain.GroupIndirect},
	{ID: "c5", Name: "Individual Supervision", Group: domain.GroupSupervision},
	{ID: "c6", Name: "Couples / Family Therapy", Group: domain.GroupDirect},
	{ID: "c7", Name: "Case Management", Group: domain.GroupIndirect},
	{ID: "c8", Name: "Group Supervision", Group: domain.GroupSupervision},
}

func newFakeStore() *fakeStore {
	return &fakeStore{categories: testCategories, failErr: errors.New("storage failed")}
}

func (s *fakeStore) Import(ctx context.Context, _ string, fn func(context.Context, importexport.Tx) error) error {
	s.opened++
	tx := &fakeTx{store: s, clients: slices.Clone(s.clients), entries: slices.Clone(s.entries), nextID: s.nextID}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	s.clients, s.entries, s.nextID = tx.clients, tx.entries, tx.nextID
	return nil
}

type fakeTx struct {
	store   *fakeStore
	clients []domain.Client
	entries []importexport.NewEntry
	nextID  int
}

func (t *fakeTx) Categories(context.Context) ([]domain.Category, error) {
	if t.store.failOn == "categories" {
		return nil, t.store.failErr
	}
	return t.store.categories, nil
}

func (t *fakeTx) MergeClient(_ context.Context, name string) (importexport.MergedClient, error) {
	if t.store.failOn == "merge" {
		return importexport.MergedClient{}, t.store.failErr
	}
	for _, c := range t.clients {
		if strings.EqualFold(c.Name, name) {
			return importexport.MergedClient{Client: c}, nil
		}
	}
	t.nextID++
	c := domain.Client{ID: fmt.Sprintf("client-%02d", t.nextID), Name: name}
	t.clients = append(t.clients, c)
	return importexport.MergedClient{Client: c, Created: true}, nil
}

func (t *fakeTx) EntriesBetween(_ context.Context, first, last time.Time) ([]importexport.ExistingEntry, error) {
	if t.store.failOn == "between" {
		return nil, t.store.failErr
	}
	var out []importexport.ExistingEntry
	for _, e := range t.store.stored {
		if !e.SessionDate.Before(first) && !e.SessionDate.After(last) {
			out = append(out, e)
		}
	}
	// Entries created by an earlier import are stored entries of later ones.
	for _, e := range t.entries {
		if !e.SessionDate.Before(first) && !e.SessionDate.After(last) {
			out = append(out, importexport.ExistingEntry{
				SessionDate: e.SessionDate, HasTimes: e.HasTimes, StartMinute: e.StartMinute, EndMinute: e.EndMinute,
				DurationMinutes: e.DurationMinutes, CategoryID: e.CategoryID, Notes: e.Notes,
				Status: domain.StatusPending, ClientIDs: e.ClientIDs,
			})
		}
	}
	return out, nil
}

func (t *fakeTx) CreateEntry(_ context.Context, e importexport.NewEntry) error {
	if t.store.failOn == "create" {
		return t.store.failErr
	}
	t.entries = append(t.entries, e)
	return nil
}

func actor() domain.Actor {
	return domain.Actor{ID: "u1", OrganizationID: "o1", Email: "trainee@example.test", Active: true}
}

func newService(t *testing.T, store importexport.Store) *importexport.Service {
	t.Helper()
	s, err := importexport.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runImport(t *testing.T, s *importexport.Service, text string) importexport.Report {
	t.Helper()
	report, err := s.Import(t.Context(), actor(), strings.NewReader(text))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	return report
}

const (
	legacyHeader = "| Date | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|\n"
	timedHeader  = "| Date | Start | End | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|---|\n"
)

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestNew_RequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := importexport.New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

func TestImport_Authorization(t *testing.T) {
	t.Parallel()
	for name, a := range map[string]domain.Actor{
		"inactive": {ID: "u1", OrganizationID: "o1"},
		"no user":  {OrganizationID: "o1", Active: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			_, err := newService(t, store).Import(t.Context(), a, strings.NewReader(legacyHeader))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("Import() error = %v, want ErrForbidden", err)
			}
			if store.opened != 0 {
				t.Fatal("a transaction was opened for a forbidden actor")
			}
		})
	}
}

func TestImport_RefusedFile(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		input string
		want  error
	}{
		"no table":    {"# Notes\nnothing here\n", importexport.ErrNoEntriesTable},
		"not UTF-8":   {legacyHeader + "| 2026-01-05 | Direct Service | Individual Counseling | — | 1 | No | \xff |\n", importexport.ErrNotText},
		"too large":   {strings.Repeat("a", importexport.MaxFileBytes+1), importexport.ErrTooLarge},
		"empty input": {"", importexport.ErrNoEntriesTable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			_, err := newService(t, store).Import(t.Context(), actor(), strings.NewReader(tc.input))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Import() error = %v, want %v", err, tc.want)
			}
			if store.opened != 0 {
				t.Fatal("a transaction was opened for a refused file")
			}
		})
	}
}

func TestImport_LegacyRowsNeedTime(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1.5 | Yes | Intake follow-up |\n"+
		"| 2026-01-06 | Indirect Service | Writing Progress Notes / Documentation | — | 0.25 | No |  |\n")

	if report.TotalRows != 2 || report.ImportedCount != 2 || report.NeedsTimeCount != 2 || len(report.SkippedRows) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.entries) != 2 {
		t.Fatalf("stored %d entries, want 2", len(store.entries))
	}
	first, second := store.entries[0], store.entries[1]
	wantFirst := importexport.NewEntry{
		SessionDate: day("2026-01-05"), DurationMinutes: 90, CategoryID: "c1", ClientIDs: []string{"client-01"}, OnSite: true, Notes: "Intake follow-up",
	}
	wantSecond := importexport.NewEntry{SessionDate: day("2026-01-06"), DurationMinutes: 15, CategoryID: "c3", ClientIDs: []string{}}
	for name, pair := range map[string][2]importexport.NewEntry{"first": {first, wantFirst}, "second": {second, wantSecond}} {
		got, want := pair[0], pair[1]
		if got.HasTimes || !got.SessionDate.Equal(want.SessionDate) || got.DurationMinutes != want.DurationMinutes ||
			got.CategoryID != want.CategoryID || got.OnSite != want.OnSite || got.Notes != want.Notes ||
			!slices.Equal(got.ClientIDs, want.ClientIDs) {
			t.Fatalf("%s entry = %+v, want %+v", name, got, want)
		}
	}
}

func TestImport_RowsWithTimes(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), timedHeader+
		"| 2026-02-02 | 09:00 | 10:30 | Direct Service | Individual Counseling | Pseudonym A | 1.5 | Yes | Weekly session |\n"+
		"| 2026-02-03 |  |  | Indirect Service | Staff Meetings | — | 0.5 | Yes | Imported without times |\n")

	if report.ImportedCount != 2 || report.NeedsTimeCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	timed, legacy := store.entries[0], store.entries[1]
	if !timed.HasTimes || timed.StartMinute != 540 || timed.EndMinute != 630 || timed.DurationMinutes != 90 {
		t.Fatalf("timed entry = %+v", timed)
	}
	if legacy.HasTimes || legacy.DurationMinutes != 30 {
		t.Fatalf("legacy entry = %+v", legacy)
	}
}

func TestImport_MergesClientsIgnoringCase(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.clients = []domain.Client{{ID: "saved-1", Name: "pseudonym a"}}
	text := "## Clients / Groups\n- Pseudonym B\n- Pseudonym A\n\n## Entries\n" + legacyHeader +
		"| 2026-01-05 | Direct Service | Group Facilitation | PSEUDONYM A; Tuesday Group | 1 | No |  |\n" +
		"| 2026-01-06 | Direct Service | Individual Counseling | tuesday group; Pseudonym C | 1 | No |  |\n"
	report := runImport(t, newService(t, store), text)

	var got []string
	for _, m := range report.MergedClients {
		got = append(got, fmt.Sprintf("%s:%s:%t", m.Client.Name, m.Client.ID, m.Created))
	}
	want := []string{"pseudonym a:saved-1:false", "Pseudonym B:client-01:true", "Pseudonym C:client-02:true", "Tuesday Group:client-03:true"}
	// The saved spelling wins for a name the user already had; new names keep the file's first spelling.
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("merged clients = %v, want %v", got, want)
	}
	if len(store.clients) != 4 {
		t.Fatalf("user has %d clients, want 4 (no case-insensitive duplicates)", len(store.clients))
	}
	// The report is ordered by name ignoring case.
	names := make([]string, len(report.MergedClients))
	for i, m := range report.MergedClients {
		names[i] = strings.ToLower(m.Client.Name)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("merged clients not ordered by name: %v", names)
	}
	// Both rows with the Tuesday Group use the same client, and the entry gets the saved client for PSEUDONYM A.
	if !slices.Contains(store.entries[0].ClientIDs, "saved-1") {
		t.Fatalf("first entry clients = %v, want the saved client", store.entries[0].ClientIDs)
	}
	if !slices.Contains(store.entries[0].ClientIDs, "client-03") || !slices.Contains(store.entries[1].ClientIDs, "client-03") {
		t.Fatalf("Tuesday Group is not one client: %v %v", store.entries[0].ClientIDs, store.entries[1].ClientIDs)
	}
}

func TestImport_ClientsOfSkippedRowsAreStillMerged(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), legacyHeader+
		"| 2026-01-05 | Direct Service | Not A Category | Pseudonym A | 1 | No |  |\n")
	if report.ImportedCount != 0 || len(report.MergedClients) != 1 || !report.MergedClients[0].Created {
		t.Fatalf("report = %+v", report)
	}
}

func TestImport_SkipsDuplicates(t *testing.T) {
	t.Parallel()
	text := legacyHeader +
		"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A; Pseudonym B | 1.5 | Yes | Intake follow-up |\n" +
		"| 2026-01-06 | Indirect Service | Staff Meetings | — | 0.5 | No |  |\n"
	store := newFakeStore()
	s := newService(t, store)
	first := runImport(t, s, text)
	if first.ImportedCount != 2 {
		t.Fatalf("first import = %+v", first)
	}

	again := runImport(t, s, text)
	if again.ImportedCount != 0 || again.NeedsTimeCount != 0 || again.TotalRows != 2 || len(again.SkippedRows) != 2 {
		t.Fatalf("second import = %+v", again)
	}
	for i, row := range again.SkippedRows {
		if row.Reason != importexport.ReasonDuplicate || row.Row != i+1 || row.Field != "" || row.Message == "" {
			t.Fatalf("skipped row %d = %+v", i, row)
		}
	}
	if len(store.entries) != 2 {
		t.Fatalf("re-import stored %d entries, want 2", len(store.entries))
	}
	for _, m := range again.MergedClients {
		if m.Created {
			t.Fatalf("re-import created client %q", m.Client.Name)
		}
	}
}

func TestImport_DuplicateRules(t *testing.T) {
	t.Parallel()
	base := "| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | note |\n"
	cases := map[string]struct {
		row      string
		duplicat bool
	}{
		"identical":                {base, true},
		"on-site is not compared":  {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | No | note |\n", true},
		"client name case":         {"| 2026-01-05 | Direct Service | Individual Counseling | PSEUDONYM A | 1 | Yes | note |\n", true},
		"category name case":       {"| 2026-01-05 | Direct Service | individual counseling | Pseudonym A | 1 | Yes | note |\n", true},
		"different date":           {"| 2026-01-06 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | note |\n", false},
		"different category":       {"| 2026-01-05 | Direct Service | Group Facilitation | Pseudonym A | 1 | Yes | note |\n", false},
		"different hours":          {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1.25 | Yes | note |\n", false},
		"different notes":          {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | other |\n", false},
		"different case in notes":  {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | NOTE |\n", false},
		"different clients":        {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym B | 1 | Yes | note |\n", false},
		"extra client":             {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A; Pseudonym B | 1 | Yes | note |\n", false},
		"no clients":               {"| 2026-01-05 | Direct Service | Individual Counseling | — | 1 | Yes | note |\n", false},
		"client order is not used": {"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A; Pseudonym B | 1 | Yes | note |\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			s := newService(t, store)
			runImport(t, s, legacyHeader+base)
			report := runImport(t, s, legacyHeader+tc.row)
			if got := len(report.SkippedRows) == 1 && report.SkippedRows[0].Reason == importexport.ReasonDuplicate; got != tc.duplicat {
				t.Fatalf("duplicate = %t, want %t; report = %+v", got, tc.duplicat, report)
			}
		})
	}
}

func TestImport_ClientSetIgnoresOrder(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newService(t, store)
	runImport(t, s, legacyHeader+"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A; Pseudonym B | 1 | Yes | n |\n")
	report := runImport(t, s, legacyHeader+"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym B; Pseudonym A | 1 | Yes | n |\n")
	if report.ImportedCount != 0 || len(report.SkippedRows) != 1 || report.SkippedRows[0].Reason != importexport.ReasonDuplicate {
		t.Fatalf("report = %+v", report)
	}
}

// A ledger can hold the same session twice. Both import the first time, and both are skipped the
// second time: duplicates are matched one to one with stored entries.
func TestImport_RepeatedSessionInOneFile(t *testing.T) {
	t.Parallel()
	row := "| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | note |\n"
	text := legacyHeader + row + row + row
	store := newFakeStore()
	s := newService(t, store)

	first := runImport(t, s, text)
	if first.ImportedCount != 3 || len(first.SkippedRows) != 0 {
		t.Fatalf("first import = %+v", first)
	}
	second := runImport(t, s, text)
	if second.ImportedCount != 0 || len(second.SkippedRows) != 3 {
		t.Fatalf("second import = %+v", second)
	}
	// A file with four of them adds only the extra one.
	third := runImport(t, s, legacyHeader+row+row+row+row)
	if third.ImportedCount != 1 || len(third.SkippedRows) != 3 || third.SkippedRows[0].Row != 1 {
		t.Fatalf("third import = %+v", third)
	}
	if len(store.entries) != 4 {
		t.Fatalf("stored %d entries, want 4", len(store.entries))
	}
}

// Adding a time to an imported entry (S4-05) must not make the next import of the old file add it again.
func TestImport_StoredEntryWithTimesMatchesLegacyRow(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.stored = []importexport.ExistingEntry{{
		SessionDate: day("2026-01-05"), HasTimes: true, StartMinute: 540, EndMinute: 630, DurationMinutes: 90,
		CategoryID: "c1", Notes: "Intake follow-up", Status: domain.StatusApproved,
	}}
	report := runImport(t, newService(t, store), legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | — | 1.5 | Yes | Intake follow-up |\n")
	if report.ImportedCount != 0 || report.SkippedRows[0].Reason != importexport.ReasonDuplicate {
		t.Fatalf("report = %+v", report)
	}
}

func TestImport_RejectedStoredEntryStillCountsAsDuplicate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.stored = []importexport.ExistingEntry{{
		SessionDate: day("2026-01-05"), DurationMinutes: 60, CategoryID: "c1", Status: domain.StatusRejected,
	}}
	report := runImport(t, newService(t, store), legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | — | 1 | No |  |\n")
	if report.ImportedCount != 0 || report.SkippedRows[0].Reason != importexport.ReasonDuplicate {
		t.Fatalf("report = %+v", report)
	}
}

func TestImport_Overlaps(t *testing.T) {
	t.Parallel()
	stored := importexport.ExistingEntry{
		SessionDate: day("2026-02-02"), HasTimes: true, StartMinute: 600, EndMinute: 660, DurationMinutes: 60,
		CategoryID: "c4", Notes: "stored", Status: domain.StatusPending,
	}
	rejected := stored
	rejected.Status = domain.StatusRejected
	legacyStored := importexport.ExistingEntry{SessionDate: day("2026-02-02"), DurationMinutes: 600, CategoryID: "c4", Notes: "legacy"}

	cases := map[string]struct {
		stored  []importexport.ExistingEntry
		start   string
		end     string
		day     string
		overlap bool
	}{
		"inside a stored session":    {[]importexport.ExistingEntry{stored}, "10:15", "10:45", "2026-02-02", true},
		"covers a stored session":    {[]importexport.ExistingEntry{stored}, "09:00", "12:00", "2026-02-02", true},
		"starts inside":              {[]importexport.ExistingEntry{stored}, "10:30", "11:30", "2026-02-02", true},
		"ends inside":                {[]importexport.ExistingEntry{stored}, "09:30", "10:15", "2026-02-02", true},
		"back to back after":         {[]importexport.ExistingEntry{stored}, "11:00", "12:00", "2026-02-02", false},
		"back to back before":        {[]importexport.ExistingEntry{stored}, "09:00", "10:00", "2026-02-02", false},
		"another day":                {[]importexport.ExistingEntry{stored}, "10:00", "11:00", "2026-02-03", false},
		"stored rejected is ignored": {[]importexport.ExistingEntry{rejected}, "10:00", "11:00", "2026-02-02", false},
		"stored legacy is ignored":   {[]importexport.ExistingEntry{legacyStored}, "10:00", "11:00", "2026-02-02", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.stored = tc.stored
			report := runImport(t, newService(t, store), timedHeader+
				fmt.Sprintf("| %s | %s | %s | Direct Service | Individual Counseling | — |  | No | new |\n", tc.day, tc.start, tc.end))
			if tc.overlap {
				if report.ImportedCount != 0 || len(report.SkippedRows) != 1 || report.SkippedRows[0].Reason != importexport.ReasonOverlap || report.SkippedRows[0].Field != "" {
					t.Fatalf("report = %+v, want one overlap", report)
				}
				return
			}
			if report.ImportedCount != 1 || len(report.SkippedRows) != 0 {
				t.Fatalf("report = %+v, want the row imported", report)
			}
		})
	}
}

func TestImport_OverlapWithinTheFile(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), timedHeader+
		"| 2026-02-02 | 09:00 | 10:00 | Direct Service | Individual Counseling | — | 1 | No | a |\n"+
		"| 2026-02-02 | 09:30 | 10:30 | Direct Service | Group Facilitation | — | 1 | No | b |\n"+
		"| 2026-02-02 | 10:00 | 11:00 | Direct Service | Group Facilitation | — | 1 | No | c |\n"+
		"| 2026-02-02 |  |  | Direct Service | Group Facilitation | — | 1 | No | d |\n")
	if report.ImportedCount != 3 || report.NeedsTimeCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.SkippedRows) != 1 || report.SkippedRows[0].Row != 2 || report.SkippedRows[0].Reason != importexport.ReasonOverlap {
		t.Fatalf("skipped = %+v", report.SkippedRows)
	}
}

func TestImport_ReimportOfTimedFileIsAllDuplicates(t *testing.T) {
	t.Parallel()
	text := timedHeader +
		"| 2026-02-02 | 09:00 | 10:30 | Direct Service | Individual Counseling | Pseudonym A | 1.5 | Yes | Weekly session |\n" +
		"| 2026-02-02 | 10:45 | 11:30 | Direct Service | Individual Counseling | Pseudonym A | 0.75 | No | Calls |\n"
	store := newFakeStore()
	s := newService(t, store)
	runImport(t, s, text)
	report := runImport(t, s, text)
	if report.ImportedCount != 0 || len(report.SkippedRows) != 2 {
		t.Fatalf("report = %+v", report)
	}
	for _, row := range report.SkippedRows {
		if row.Reason != importexport.ReasonDuplicate {
			t.Fatalf("row %d reason = %s, want duplicate (not overlap with itself)", row.Row, row.Reason)
		}
	}
}

func TestImport_Categories(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		group, category string
		wantField       string
		ok              bool
	}{
		"exact":                {"Direct Service", "Individual Counseling", "", true},
		"case and padding":     {"direct service", "  INDIVIDUAL counseling ", "", true},
		"group is not checked": {"Supervision", "Individual Counseling", "", true},
		"unknown category":     {"Direct Service", "Teaching", "category", false},
		"category of a prefix": {"Direct Service", "Individual", "category", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			report := runImport(t, newService(t, store), legacyHeader+
				fmt.Sprintf("| 2026-01-05 | %s | %s | — | 1 | No |  |\n", tc.group, tc.category))
			if tc.ok {
				if report.ImportedCount != 1 || store.entries[0].CategoryID != "c1" {
					t.Fatalf("report = %+v, entries = %+v", report, store.entries)
				}
				return
			}
			if report.ImportedCount != 0 || len(report.SkippedRows) != 1 {
				t.Fatalf("report = %+v", report)
			}
			got := report.SkippedRows[0]
			if got.Reason != importexport.ReasonUnknownCategory || got.Field != tc.wantField || got.Message == "" {
				t.Fatalf("skipped row = %+v", got)
			}
			if strings.Contains(got.Message, tc.category) && len(strings.TrimSpace(tc.category)) > 3 {
				t.Fatalf("message quotes the cell: %q", got.Message)
			}
		})
	}
}

// Bad rows are reported and the valid rows are kept, with every report row in file order.
func TestImport_BadRowsAreReportedAndGoodRowsKept(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | one |\n"+
		"| 2026-02-30 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | impossible date |\n"+
		"| 2026-01-07 | Direct Service | Teaching | Pseudonym A | 1 | Yes | unknown category |\n"+
		"| 2026-01-08 | Direct Service | Individual Counseling | Pseudonym A | 0 | Yes | zero hours |\n"+
		"| 2026-01-09 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | two |\n"+
		"| 2026-01-09 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | two |\n"+
		"| 2026-01-10 | Direct Service | Individual Counseling |\n")

	if report.TotalRows != 7 || report.ImportedCount != 3 || len(report.SkippedRows) != 4 {
		t.Fatalf("report = %+v", report)
	}
	var got []string
	for _, r := range report.SkippedRows {
		got = append(got, fmt.Sprintf("%d:%s", r.Row, r.Reason))
	}
	want := []string{"2:invalid-date", "3:unknown-category", "4:invalid-hours", "7:malformed-row"}
	if !slices.Equal(got, want) {
		t.Fatalf("skipped = %v, want %v", got, want)
	}
	if report.ImportedCount+len(report.SkippedRows) != report.TotalRows {
		t.Fatal("imported + skipped is not the total")
	}
	if len(store.entries) != 3 {
		t.Fatalf("stored %d entries, want 3", len(store.entries))
	}
}

func TestImport_ReportNeverQuotesCells(t *testing.T) {
	t.Parallel()
	secret := "ZZ-secret-note"
	store := newFakeStore()
	report := runImport(t, newService(t, store), legacyHeader+
		fmt.Sprintf("| 2026-01-05 | Direct Service | %s | Pseudonym A | 1 | Yes | %s |\n", secret, secret)+
		fmt.Sprintf("| 2026-01-06 | Direct Service | Individual Counseling | Pseudonym A | %s | Yes | %s |\n", secret, secret))
	for _, row := range report.SkippedRows {
		if strings.Contains(row.Message, secret) {
			t.Fatalf("message quotes a cell: %q", row.Message)
		}
	}
	if len(report.SkippedRows) != 2 {
		t.Fatalf("skipped = %+v", report.SkippedRows)
	}
}

func TestImport_InjectionLikeCellsAreOnlyText(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notes := []string{`' OR 1=1 --`, `'; DROP TABLE entries; --`, `=HYPERLINK("http://example.test")`, `<script>alert(1)</script>`, `100% _done_ \ back`}
	var b strings.Builder
	b.WriteString(legacyHeader)
	for i, n := range notes {
		fmt.Fprintf(&b, "| 2026-01-%02d | Direct Service | Individual Counseling | x' OR '1'='1 | 1 | No | %s |\n", i+1, strings.ReplaceAll(n, "|", `\|`))
	}
	report := runImport(t, newService(t, store), b.String())
	if report.ImportedCount != len(notes) {
		t.Fatalf("report = %+v", report)
	}
	for i, n := range notes {
		if store.entries[i].Notes != n {
			t.Fatalf("notes %d = %q, want %q unchanged", i, store.entries[i].Notes, n)
		}
	}
	if len(store.clients) != 1 || store.clients[0].Name != "x' OR '1'='1" {
		t.Fatalf("clients = %+v", store.clients)
	}
}

func TestImport_StorageFailureStoresNothing(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"categories", "merge", "between", "create"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.failOn = step
			report, err := newService(t, store).Import(t.Context(), actor(), strings.NewReader(legacyHeader+
				"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | one |\n"))
			if !errors.Is(err, store.failErr) {
				t.Fatalf("Import() error = %v, want the storage error", err)
			}
			if report.ImportedCount != 0 || len(report.SkippedRows) != 0 || len(store.entries) != 0 || len(store.clients) != 0 {
				t.Fatalf("report = %+v, entries = %d, clients = %d: a failed import must leave nothing", report, len(store.entries), len(store.clients))
			}
		})
	}
}

func TestImport_ConflictFromStoreIsReturned(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.failOn, store.failErr = "create", fmt.Errorf("overlap: %w", domain.ErrConflict)
	_, err := newService(t, store).Import(t.Context(), actor(), strings.NewReader(legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | — | 1 | Yes | one |\n"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Import() error = %v, want ErrConflict", err)
	}
}

func TestImport_ClientsOnlyFile(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	report := runImport(t, newService(t, store), "## Clients / Groups\n- Pseudonym A\n\n## Entries\n"+legacyHeader)
	if report.TotalRows != 0 || report.ImportedCount != 0 || len(report.MergedClients) != 1 || !report.MergedClients[0].Created {
		t.Fatalf("report = %+v", report)
	}
}

// The fixtures of the parser tests import cleanly through the service.
func TestImport_ParserFixtures(t *testing.T) {
	t.Parallel()
	for file, want := range map[string]struct{ imported, needsTime, skipped, clients int }{
		"testdata/prototype_no_times.md": {5, 5, 0, 4},
		"testdata/with_times.md":         {4, 1, 0, 2},
	} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			report := runImport(t, newService(t, newFakeStore()), string(data))
			if report.ImportedCount != want.imported || report.NeedsTimeCount != want.needsTime ||
				len(report.SkippedRows) != want.skipped || len(report.MergedClients) != want.clients {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}
