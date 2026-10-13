package loghoursqueries_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	lq "github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

// S2-02 tests: every query runs against real PostgreSQL with the real migrations (GO-72). Users,
// clients and notes are fictional pseudonyms (CLAUDE.md §0.7).

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

// fixture is one migrated database with a handle that runs the generated queries.
type fixture struct {
	conn *pgx.Conn
	q    *lq.Queries
	ctx  context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := server.NewDatabase(t)
	pool, err := db.Open(t.Context(), db.Settings{
		Host: server.Host, Port: server.Port, Database: database, User: server.User,
		Mode: db.AuthPassword, Password: server.Password,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	res := migrate.Run(t.Context(), sqlDB, migrations.FS, migrate.Options{})
	_ = sqlDB.Close()
	pool.Close()
	if res.Status != migrate.StatusOK {
		t.Fatalf("migrate: %+v", res)
	}
	conn := server.Connect(t, database)
	return &fixture{conn: conn, q: lq.New(conn), ctx: t.Context()}
}

func (f *fixture) user(t *testing.T, email string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	err := f.conn.QueryRow(f.ctx,
		`INSERT INTO users (organization_id, email) SELECT id, $1 FROM organizations WHERE name = 'Default Organization' RETURNING id`,
		email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// categoryID returns the category with this display position (CLAUDE.md §8): 1 Individual Counseling,
// 2 Group Facilitation, 6 Writing Progress Notes, 7 Staff Meetings, 11 Group Supervision.
func (f *fixture) categoryID(t *testing.T, sortOrder int) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := f.conn.QueryRow(f.ctx, `SELECT id FROM categories WHERE sort_order = $1`, sortOrder).Scan(&id); err != nil {
		t.Fatalf("category %d: %v", sortOrder, err)
	}
	return id
}

func (f *fixture) client(t *testing.T, user pgtype.UUID, name string) lq.Client {
	t.Helper()
	c, err := f.q.InsertClient(f.ctx, lq.InsertClientParams{UserID: user, Name: name})
	if err != nil {
		t.Fatalf("insert client %q: %v", name, err)
	}
	return c
}

func (f *fixture) setStatus(t *testing.T, id pgtype.UUID, status string) {
	t.Helper()
	if _, err := f.conn.Exec(f.ctx, `UPDATE entries SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func day(s string) pgtype.Date {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return pgtype.Date{Time: d, Valid: true}
}

func clock(s string) pgtype.Time {
	d, err := time.Parse("15:04", s)
	if err != nil {
		panic(err)
	}
	return pgtype.Time{Microseconds: int64(d.Hour()*60+d.Minute()) * 60 * 1_000_000, Valid: true}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// spec describes one entry to insert; an empty start means a legacy entry without times.
type spec struct {
	date, start, end string
	minutes          int32
	category         int
	onSite           bool
	notes            string
	clients          []lq.Client
}

func (f *fixture) entry(t *testing.T, user pgtype.UUID, s spec) lq.Entry {
	t.Helper()
	e, err := f.tryEntry(user, s)
	if err != nil {
		t.Fatalf("insert entry %+v: %v", s, err)
	}
	return e
}

func (f *fixture) tryEntry(user pgtype.UUID, s spec) (lq.Entry, error) {
	p := lq.InsertEntryParams{
		UserID: user, SessionDate: day(s.date), DurationMinutes: s.minutes,
		CategoryID: pgtype.UUID{}, OnSite: s.onSite,
	}
	var cat pgtype.UUID
	if err := f.conn.QueryRow(f.ctx, `SELECT id FROM categories WHERE sort_order = $1`, s.category).Scan(&cat); err != nil {
		return lq.Entry{}, err
	}
	p.CategoryID = cat
	if s.start != "" {
		p.StartTime, p.EndTime = clock(s.start), clock(s.end)
	}
	if s.notes != "" {
		p.Notes = text(s.notes)
	}
	e, err := f.q.InsertEntry(f.ctx, p)
	if err != nil {
		return lq.Entry{}, err
	}
	if len(s.clients) > 0 {
		ids := make([]pgtype.UUID, len(s.clients))
		for i, c := range s.clients {
			ids[i] = c.ID
		}
		if err := f.q.InsertEntryClients(f.ctx, lq.InsertEntryClientsParams{EntryID: e.ID, UserID: user, ClientIds: ids}); err != nil {
			return lq.Entry{}, err
		}
	}
	return e, nil
}

func wantSQLState(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want SQLSTATE %s on %s", err, code, constraint)
	}
	if pgErr.Code != code || (constraint != "" && pgErr.ConstraintName != constraint) {
		t.Fatalf("SQLSTATE %s on %q (%s), want %s on %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}

func wantNoRows(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
}

// ids maps entries to short labels so assertions read as orders.
func labels(entries []lq.Entry, byID map[pgtype.UUID]string) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = byID[e.ID]
	}
	return out
}

func TestListCategories_TheTwelveInDisplayOrder(t *testing.T) {
	f := newFixture(t)
	cats, err := f.q.ListCategories(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, c := range cats {
		if int(c.SortOrder) != i+1 {
			t.Errorf("category %q has sort_order %d at position %d", c.Name, c.SortOrder, i+1)
		}
		got = append(got, c.CategoryGroup+":"+c.Name)
	}
	want := []string{
		"direct:Individual Counseling", "direct:Group Facilitation", "direct:Intake Interviews",
		"direct:Couples / Family Therapy", "direct:Psychoeducation",
		"indirect:Writing Progress Notes / Documentation", "indirect:Staff Meetings", "indirect:Case Management",
		"indirect:Assessment Scoring", "indirect:Professional Development",
		"supervision:Group Supervision", "supervision:Individual Supervision",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("categories = %q\nwant %q", got, want)
	}
}

func TestClients_AreScopedToTheirOwnerAndUniqueIgnoringCase(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "pseudonym-a@example.test"), f.user(t, "pseudonym-b@example.test")

	f.client(t, a, "Zed")
	f.client(t, a, "alpha")
	f.client(t, a, "Bravo")
	f.client(t, b, "Other Group")

	list, err := f.q.ListClients(f.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if want := []string{"alpha", "Bravo", "Zed"}; !slices.Equal(names, want) {
		t.Fatalf("clients = %q, want %q (own clients only, ordered ignoring case)", names, want)
	}

	// 'a.b.' when 'A.B.' exists is refused by the database; the caller can fetch the existing one.
	existing := f.client(t, a, "A.B.")
	_, err = f.q.InsertClient(f.ctx, lq.InsertClientParams{UserID: a, Name: "a.b."})
	wantSQLState(t, err, "23505", "clients_user_id_lower_name_idx")
	got, err := f.q.GetClientByName(f.ctx, lq.GetClientByNameParams{UserID: a, Name: "a.B."})
	if err != nil || got.ID != existing.ID || got.Name != "A.B." {
		t.Fatalf("GetClientByName = %+v, %v; want the existing %+v", got, err, existing)
	}
	// The same name under another user is a different client, and is not visible across users.
	if _, err := f.q.InsertClient(f.ctx, lq.InsertClientParams{UserID: b, Name: "a.b."}); err != nil {
		t.Fatalf("same name for another user: %v", err)
	}
	_, err = f.q.GetClientByName(f.ctx, lq.GetClientByNameParams{UserID: b, Name: "Zed"})
	wantNoRows(t, err)
}

// wantDeleted deletes an entry and fails unless exactly rows rows were removed.
func (f *fixture) wantDeleted(t *testing.T, id, user pgtype.UUID, rows int64) {
	t.Helper()
	if n, err := f.q.DeleteEntry(f.ctx, lq.DeleteEntryParams{ID: id, UserID: user}); err != nil || n != rows {
		t.Fatalf("delete = %d rows, %v; want %d", n, err, rows)
	}
}

func TestEntries_InsertAndGetAreOwnerOnly(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "pseudonym-a@example.test"), f.user(t, "pseudonym-b@example.test")

	timed := f.entry(t, a, spec{date: "2026-03-02", start: "09:00", end: "10:30", minutes: 90, category: 1, notes: "Fictional note"})
	if timed.Status != "pending" || timed.NeedsTime || timed.UserID != a || timed.DurationMinutes != 90 {
		t.Fatalf("inserted entry = %+v", timed)
	}
	legacy := f.entry(t, a, spec{date: "2026-03-02", minutes: 45, category: 2})
	if !legacy.NeedsTime || legacy.StartTime.Valid || legacy.EndTime.Valid {
		t.Fatalf("an entry without times must be flagged needs_time: %+v", legacy)
	}

	got, err := f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: timed.ID, UserID: a})
	if err != nil || got.ID != timed.ID {
		t.Fatalf("owner read: %+v, %v", got, err)
	}
	_, err = f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: timed.ID, UserID: b})
	wantNoRows(t, err)
}

func TestEntries_UpdateChangesOnlyTheOwnersEntry(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "pseudonym-a@example.test"), f.user(t, "pseudonym-b@example.test")
	timed := f.entry(t, a, spec{date: "2026-03-02", start: "09:00", end: "10:30", minutes: 90, category: 1, notes: "Fictional note"})
	change := lq.UpdateEntryParams{
		ID: timed.ID, UserID: b, SessionDate: day("2026-03-03"), StartTime: clock("14:00"), EndTime: clock("15:00"),
		DurationMinutes: 60, CategoryID: f.categoryID(t, 7), OnSite: true, Notes: text("Changed"),
	}

	_, err := f.q.UpdateEntry(f.ctx, change)
	wantNoRows(t, err) // someone else's entry
	untouched, err := f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: timed.ID, UserID: a})
	if err != nil || untouched.Notes.String != "Fictional note" {
		t.Fatalf("a stranger changed the entry: %+v, %v", untouched, err)
	}

	change.UserID = a
	updated, err := f.q.UpdateEntry(f.ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SessionDate != day("2026-03-03") || updated.StartTime != clock("14:00") || updated.DurationMinutes != 60 ||
		!updated.OnSite || updated.Notes.String != "Changed" || updated.NeedsTime || updated.CategoryID != change.CategoryID {
		t.Fatalf("updated entry = %+v", updated)
	}
	if !updated.UpdatedAt.Time.After(timed.UpdatedAt.Time) {
		t.Errorf("updated_at did not move: %v then %v", timed.UpdatedAt.Time, updated.UpdatedAt.Time)
	}

	// A save without times keeps the flag in step with entries_needs_time_chk.
	change.StartTime, change.EndTime, change.DurationMinutes = pgtype.Time{}, pgtype.Time{}, 45
	if e, err := f.q.UpdateEntry(f.ctx, change); err != nil || !e.NeedsTime {
		t.Fatalf("update without times = %+v, %v", e, err)
	}
}

func TestEntries_DeleteRemovesOnlyTheOwnersEntry(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "pseudonym-a@example.test"), f.user(t, "pseudonym-b@example.test")
	e := f.entry(t, a, spec{date: "2026-03-02", start: "09:00", end: "10:30", minutes: 90, category: 1})

	f.wantDeleted(t, e.ID, b, 0)
	if _, err := f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: e.ID, UserID: a}); err != nil {
		t.Fatalf("a stranger's delete removed the entry: %v", err)
	}
	f.wantDeleted(t, e.ID, a, 1)
	_, err := f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: e.ID, UserID: a})
	wantNoRows(t, err)
	f.wantDeleted(t, e.ID, a, 0)
}

func TestEntries_ApprovedAreLockedAndRejectedReturnToPending(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	e := f.entry(t, a, spec{date: "2026-03-02", start: "09:00", end: "10:00", minutes: 60, category: 1})
	save := lq.UpdateEntryParams{
		ID: e.ID, UserID: a, SessionDate: e.SessionDate, StartTime: e.StartTime, EndTime: e.EndTime,
		DurationMinutes: 60, CategoryID: e.CategoryID, Notes: text("Edited"),
	}

	f.setStatus(t, e.ID, "approved")
	_, err := f.q.UpdateEntry(f.ctx, save)
	wantNoRows(t, err)
	f.wantDeleted(t, e.ID, a, 0)
	locked, _ := f.q.GetEntry(f.ctx, lq.GetEntryParams{ID: e.ID, UserID: a})
	if locked.Status != "approved" || locked.Notes.Valid {
		t.Fatalf("approved entry changed: %+v", locked)
	}

	f.setStatus(t, e.ID, "rejected")
	back, err := f.q.UpdateEntry(f.ctx, save)
	if err != nil || back.Status != "pending" || back.Notes.String != "Edited" {
		t.Fatalf("edited rejected entry = %+v, %v; want status pending", back, err)
	}

	f.setStatus(t, e.ID, "rejected")
	f.wantDeleted(t, e.ID, a, 1)
}

func TestEntries_OverlapsAreRejectedByTheDatabaseAndFoundByTheLookup(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t, "pseudonym-a@example.test"), f.user(t, "pseudonym-b@example.test")
	first := f.entry(t, a, spec{date: "2026-03-02", start: "10:00", end: "11:00", minutes: 60, category: 1})

	_, err := f.tryEntry(a, spec{date: "2026-03-02", start: "10:30", end: "11:30", minutes: 60, category: 2})
	wantSQLState(t, err, "23P01", "entries_no_overlap_excl")
	// Back-to-back sessions, other days and other users do not collide.
	f.entry(t, a, spec{date: "2026-03-02", start: "11:00", end: "12:00", minutes: 60, category: 2})
	f.entry(t, a, spec{date: "2026-03-03", start: "10:00", end: "11:00", minutes: 60, category: 2})
	f.entry(t, b, spec{date: "2026-03-02", start: "10:00", end: "11:00", minutes: 60, category: 2})

	find := lq.FindOverlappingEntryParams{UserID: a, SessionDate: day("2026-03-02"), StartTime: clock("10:30"), EndTime: clock("11:30")}
	hit, err := f.q.FindOverlappingEntry(f.ctx, find)
	if err != nil || hit.ID != first.ID || hit.StartTime != clock("10:00") || hit.EndTime != clock("11:00") || hit.SessionDate != day("2026-03-02") {
		t.Fatalf("overlap lookup = %+v, %v; want the 10:00-11:00 entry", hit, err)
	}
	// Excluding the entry being edited removes the self-conflict.
	find.StartTime, find.EndTime, find.ExcludeID = clock("10:00"), clock("11:00"), first.ID
	_, err = f.q.FindOverlappingEntry(f.ctx, find)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("self overlap with exclude_id = %v, want no rows", err)
	}
	// A free slot, and another user's slot, find nothing.
	free := lq.FindOverlappingEntryParams{UserID: a, SessionDate: day("2026-03-02"), StartTime: clock("12:00"), EndTime: clock("13:00")}
	_, err = f.q.FindOverlappingEntry(f.ctx, free)
	wantNoRows(t, err)
	// A rejected entry is ignored by the lookup exactly as by the constraint.
	f.setStatus(t, first.ID, "rejected")
	find.ExcludeID = pgtype.UUID{}
	_, err = f.q.FindOverlappingEntry(f.ctx, find)
	wantNoRows(t, err)
	f.setStatus(t, first.ID, "pending")

	// Editing an entry into a neighbour's time is refused by the same constraint.
	other := f.entry(t, a, spec{date: "2026-03-04", start: "10:00", end: "11:00", minutes: 60, category: 1})
	_, err = f.q.UpdateEntry(f.ctx, lq.UpdateEntryParams{
		ID: other.ID, UserID: a, SessionDate: day("2026-03-02"), StartTime: clock("10:15"), EndTime: clock("10:45"),
		DurationMinutes: 30, CategoryID: other.CategoryID,
	})
	wantSQLState(t, err, "23P01", "entries_no_overlap_excl")
	// Saving an entry over its own time is not a conflict.
	if _, err := f.q.UpdateEntry(f.ctx, lq.UpdateEntryParams{
		ID: first.ID, UserID: a, SessionDate: first.SessionDate, StartTime: first.StartTime, EndTime: first.EndTime,
		DurationMinutes: 60, CategoryID: first.CategoryID, Notes: text("Same slot"),
	}); err != nil {
		t.Fatalf("save over own slot: %v", err)
	}
}

func TestEntries_DatabaseConstraintsStillApplyThroughTheQueries(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	_, err := f.tryEntry(a, spec{date: "2026-03-02", start: "10:00", end: "11:00", minutes: 75, category: 1})
	wantSQLState(t, err, "23514", "entries_duration_matches_times_chk")
	_, err = f.tryEntry(a, spec{date: "2026-03-02", start: "10:05", end: "11:05", minutes: 60, category: 1})
	wantSQLState(t, err, "23514", "entries_quarter_hour_chk")
}

// clientsFixture is user A with clients Zed, alpha and Bravo, entries e1 (Zed, alpha) and e2 (Bravo),
// and user B with one client of their own.
type clientsFixture struct {
	*fixture
	a, b              pgtype.UUID
	zed, alpha, bravo lq.Client
	stranger          lq.Client
	e1, e2            lq.Entry
}

func newClientsFixture(t *testing.T) *clientsFixture {
	t.Helper()
	f := newFixture(t)
	c := &clientsFixture{fixture: f, a: f.user(t, "pseudonym-a@example.test"), b: f.user(t, "pseudonym-b@example.test")}
	c.zed, c.alpha, c.bravo = f.client(t, c.a, "Zed"), f.client(t, c.a, "alpha"), f.client(t, c.a, "Bravo")
	c.stranger = f.client(t, c.b, "Not Yours")
	// A repeated client id is harmless.
	c.e1 = f.entry(t, c.a, spec{date: "2026-03-02", start: "09:00", end: "10:00", minutes: 60, category: 1, clients: []lq.Client{c.zed, c.alpha, c.alpha}})
	c.e2 = f.entry(t, c.a, spec{date: "2026-03-02", start: "11:00", end: "12:00", minutes: 60, category: 1, clients: []lq.Client{c.bravo}})
	return c
}

// lines returns "entry:name" for the clients of the given entries as user: e1's, then e2's, each in
// the order ListEntryClients returns them. The query orders by entry id, which is not a stable order
// across entries, so the lines are grouped by label here.
func (c *clientsFixture) lines(t *testing.T, user pgtype.UUID, ids ...pgtype.UUID) []string {
	t.Helper()
	rows, err := c.q.ListEntryClients(c.ctx, lq.ListEntryClientsParams{UserID: user, EntryIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	byEntry := map[string][]string{}
	for _, r := range rows {
		who := "?"
		switch r.EntryID {
		case c.e1.ID:
			who = "e1"
		case c.e2.ID:
			who = "e2"
		}
		byEntry[who] = append(byEntry[who], who+":"+r.Name)
	}
	var out []string
	for _, who := range []string{"e1", "e2", "?"} {
		out = append(out, byEntry[who]...)
	}
	return out
}

func TestListEntryClients_NamesInTableOrderAndOnlyForTheOwner(t *testing.T) {
	c := newClientsFixture(t)
	// Names come back ignoring case: alpha before Zed.
	if got, want := c.lines(t, c.a, c.e1.ID, c.e2.ID), []string{"e1:alpha", "e1:Zed", "e2:Bravo"}; !slices.Equal(got, want) {
		t.Fatalf("entry clients = %q, want %q", got, want)
	}
	if got := c.lines(t, c.b, c.e1.ID, c.e2.ID); len(got) != 0 {
		t.Fatalf("another user's lookup returned %q", got)
	}
	if got := c.lines(t, c.a); len(got) != 0 {
		t.Fatalf("an empty page returned %q", got)
	}
}

func TestEntryClients_ReplaceTheSetAndRefuseOtherUsersClients(t *testing.T) {
	c := newClientsFixture(t)
	if err := c.q.DeleteEntryClients(c.ctx, lq.DeleteEntryClientsParams{EntryID: c.e1.ID, UserID: c.a}); err != nil {
		t.Fatal(err)
	}
	if err := c.q.InsertEntryClients(c.ctx, lq.InsertEntryClientsParams{EntryID: c.e1.ID, UserID: c.a, ClientIds: []pgtype.UUID{c.bravo.ID}}); err != nil {
		t.Fatal(err)
	}
	if got, want := c.lines(t, c.a, c.e1.ID, c.e2.ID), []string{"e1:Bravo", "e2:Bravo"}; !slices.Equal(got, want) {
		t.Fatalf("after replace = %q, want %q", got, want)
	}

	// Someone else detaching is a no-op.
	if err := c.q.DeleteEntryClients(c.ctx, lq.DeleteEntryClientsParams{EntryID: c.e1.ID, UserID: c.b}); err != nil {
		t.Fatal(err)
	}
	if got := c.lines(t, c.a, c.e1.ID); len(got) != 1 {
		t.Fatalf("a stranger detached clients: %q", got)
	}

	// A client of another user cannot be attached (composite foreign key); the statement attaches nothing.
	err := c.q.InsertEntryClients(c.ctx, lq.InsertEntryClientsParams{EntryID: c.e2.ID, UserID: c.a, ClientIds: []pgtype.UUID{c.alpha.ID, c.stranger.ID}})
	wantSQLState(t, err, "23503", "")
	if got := c.lines(t, c.a, c.e2.ID); !slices.Equal(got, []string{"e2:Bravo"}) {
		t.Fatalf("a failed attach changed the entry: %q", got)
	}
}

func TestEntryClients_DeletingTheEntryKeepsTheClients(t *testing.T) {
	c := newClientsFixture(t)
	c.wantDeleted(t, c.e1.ID, c.a, 1)
	var links int
	if err := c.conn.QueryRow(c.ctx, `SELECT count(*) FROM entry_clients WHERE entry_id = $1`, c.e1.ID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("links left after delete = %d, %v", links, err)
	}
	if cs, err := c.q.ListClients(c.ctx, c.a); err != nil || len(cs) != 3 {
		t.Fatalf("clients after entry delete = %d, %v; want 3", len(cs), err)
	}
}

// listFixture is the table of entries used by the filter and sort tests. Every expected order below
// is written out by hand from this table.
//
//	label date        time         min cat                    on-site clients
//	e1    2026-03-02  09:00-10:00  60  Individual Counseling  yes     Bravo
//	e2    2026-03-02  10:00-11:30  90  Staff Meetings         no      alpha, Bravo
//	e3    2026-03-02  13:00-14:00  60  Group Supervision       yes     none
//	e4    2026-03-03  09:00-10:00  60  Group Facilitation      no      Charlie
//	e5    2026-03-03  11:00-11:15  15  Individual Counseling  no      alpha
//	e6    2026-03-03  no times     30  Writing Progress Notes no      none
//	e7    2026-03-01  09:00-10:00  60  Writing Progress Notes yes     Charlie, alpha
type listFixture struct {
	*fixture
	owner pgtype.UUID
	label map[pgtype.UUID]string
}

func newListFixture(t *testing.T) *listFixture {
	t.Helper()
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	alpha, bravo, charlie := f.client(t, a, "alpha"), f.client(t, a, "Bravo"), f.client(t, a, "Charlie")
	lf := &listFixture{fixture: f, owner: a, label: map[pgtype.UUID]string{}}
	for name, s := range map[string]spec{
		"e1": {date: "2026-03-02", start: "09:00", end: "10:00", minutes: 60, category: 1, onSite: true, clients: []lq.Client{bravo}},
		"e2": {date: "2026-03-02", start: "10:00", end: "11:30", minutes: 90, category: 7, clients: []lq.Client{bravo, alpha}},
		"e3": {date: "2026-03-02", start: "13:00", end: "14:00", minutes: 60, category: 11, onSite: true},
		"e4": {date: "2026-03-03", start: "09:00", end: "10:00", minutes: 60, category: 2, clients: []lq.Client{charlie}},
		"e5": {date: "2026-03-03", start: "11:00", end: "11:15", minutes: 15, category: 1, clients: []lq.Client{alpha}},
		"e6": {date: "2026-03-03", minutes: 30, category: 6},
		"e7": {date: "2026-03-01", start: "09:00", end: "10:00", minutes: 60, category: 6, onSite: true, clients: []lq.Client{charlie, alpha}},
	} {
		lf.label[f.entry(t, a, s).ID] = name
	}
	return lf
}

func (lf *listFixture) params() lq.ListEntriesParams {
	return lq.ListEntriesParams{UserID: lf.owner, PageSize: 50}
}

func (lf *listFixture) list(t *testing.T, sort lq.EntrySort, dir lq.SortDirection, p lq.ListEntriesParams) []string {
	t.Helper()
	got, err := lf.q.ListEntries(lf.ctx, sort, dir, p)
	if err != nil {
		t.Fatalf("list %s %s: %v", sort, dir, err)
	}
	return labels(got, lf.label)
}

func TestListEntries_EverySortAndDirectionOrdersAsDocumented(t *testing.T) {
	lf := newListFixture(t)
	want := map[lq.EntrySort]map[lq.SortDirection][]string{
		// Newest first is the default. Legacy entries (no time) follow the timed ones of their date.
		lq.SortDate: {
			lq.Desc: {"e5", "e4", "e6", "e3", "e2", "e1", "e7"},
			lq.Asc:  {"e7", "e1", "e2", "e3", "e4", "e5", "e6"},
		},
		// Group, then category name; entries of one category newest first.
		lq.SortCategory: {
			lq.Asc:  {"e4", "e5", "e1", "e2", "e6", "e7", "e3"},
			lq.Desc: {"e3", "e6", "e7", "e2", "e5", "e1", "e4"},
		},
		// Client names, ignoring case, alphabetically joined; entries without clients are always last.
		lq.SortClient: {
			lq.Asc:  {"e5", "e2", "e7", "e1", "e4", "e6", "e3"},
			lq.Desc: {"e4", "e1", "e7", "e2", "e5", "e6", "e3"},
		},
		lq.SortHours: {
			lq.Asc:  {"e5", "e6", "e4", "e3", "e1", "e7", "e2"},
			lq.Desc: {"e2", "e4", "e3", "e1", "e7", "e6", "e5"},
		},
		lq.SortOnSite: {
			lq.Asc:  {"e5", "e4", "e6", "e2", "e3", "e1", "e7"},
			lq.Desc: {"e3", "e1", "e7", "e5", "e4", "e6", "e2"},
		},
	}
	n := 0
	for sort, dirs := range want {
		for dir, order := range dirs {
			n++
			if got := lf.list(t, sort, dir, lf.params()); !slices.Equal(got, order) {
				t.Errorf("%s %s = %v, want %v", sort, dir, got, order)
			}
		}
	}
	if n != 10 {
		t.Fatalf("checked %d sort/direction pairs, want 10", n)
	}
}

func TestListEntries_OnlyAllowlistedSortsRun(t *testing.T) {
	// No database is needed: an unlisted key is refused before any query is attempted.
	q := lq.New(nil)
	for _, c := range []struct {
		sort lq.EntrySort
		dir  lq.SortDirection
	}{
		{"", lq.Asc}, {lq.SortDate, ""}, {"duration_minutes", lq.Asc}, {"notes", lq.Desc},
		{lq.SortDate, "ASC"}, {lq.SortDate, "asc; DROP TABLE entries"}, {"date; DROP TABLE entries", lq.Asc},
		{"e.id", lq.Desc}, {lq.SortHours, "sideways"},
	} {
		if _, err := q.ListEntries(t.Context(), c.sort, c.dir, lq.ListEntriesParams{}); !errors.Is(err, lq.ErrInvalidSort) {
			t.Errorf("sort %q dir %q: err = %v, want ErrInvalidSort", c.sort, c.dir, err)
		}
	}
}

func TestListEntries_FiltersCombineAndCountMatches(t *testing.T) {
	lf := newListFixture(t)
	for _, c := range []struct {
		name   string
		mutate func(*lq.ListEntriesParams)
		want   []string // newest first
	}{
		{"no filters", func(*lq.ListEntriesParams) {}, []string{"e5", "e4", "e6", "e3", "e2", "e1", "e7"}},
		{"group direct", func(p *lq.ListEntriesParams) { p.CategoryGroup = text("direct") }, []string{"e5", "e4", "e1"}},
		{"group indirect", func(p *lq.ListEntriesParams) { p.CategoryGroup = text("indirect") }, []string{"e6", "e2", "e7"}},
		{"group supervision", func(p *lq.ListEntriesParams) { p.CategoryGroup = text("supervision") }, []string{"e3"}},
		{"from and to are inclusive", func(p *lq.ListEntriesParams) { p.FromDate, p.ToDate = day("2026-03-02"), day("2026-03-02") }, []string{"e3", "e2", "e1"}},
		{"from only", func(p *lq.ListEntriesParams) { p.FromDate = day("2026-03-03") }, []string{"e5", "e4", "e6"}},
		{"to only", func(p *lq.ListEntriesParams) { p.ToDate = day("2026-03-01") }, []string{"e7"}},
		{"client text ignores case", func(p *lq.ListEntriesParams) { p.ClientText = text("ALP") }, []string{"e5", "e2", "e7"}},
		{"client text is a substring", func(p *lq.ListEntriesParams) { p.ClientText = text("li") }, []string{"e4", "e7"}},
		{"client text matches no entry without clients", func(p *lq.ListEntriesParams) { p.ClientText = text("zzz") }, nil},
		{"all filters together", func(p *lq.ListEntriesParams) {
			p.CategoryGroup, p.ClientText, p.FromDate, p.ToDate = text("indirect"), text("alpha"), day("2026-03-02"), day("2026-03-03")
		}, []string{"e2"}},
		{"empty range", func(p *lq.ListEntriesParams) { p.FromDate, p.ToDate = day("2026-04-01"), day("2026-03-01") }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := lf.params()
			c.mutate(&p)
			if got := lf.list(t, lq.SortDate, lq.Desc, p); !slices.Equal(got, c.want) {
				t.Errorf("entries = %v, want %v", got, c.want)
			}
			total, err := lf.q.CountEntries(lf.ctx, lq.CountEntriesParams{
				UserID: p.UserID, CategoryGroup: p.CategoryGroup, FromDate: p.FromDate, ToDate: p.ToDate, ClientText: p.ClientText,
			})
			if err != nil || int(total) != len(c.want) {
				t.Errorf("count = %d, %v; want %d", total, err, len(c.want))
			}
			// The same filters apply to every one of the ten fixed queries: only the order differs.
			wantSet := slices.Sorted(slices.Values(c.want))
			for _, sort := range []lq.EntrySort{lq.SortDate, lq.SortCategory, lq.SortClient, lq.SortHours, lq.SortOnSite} {
				for _, dir := range []lq.SortDirection{lq.Asc, lq.Desc} {
					got := slices.Sorted(slices.Values(lf.list(t, sort, dir, p)))
					if !slices.Equal(got, wantSet) {
						t.Errorf("%s %s returned %v, want the same entries as %v", sort, dir, got, wantSet)
					}
				}
			}
		})
	}
}

func TestListEntries_PagesAreWindowsOfTheSameOrder(t *testing.T) {
	lf := newListFixture(t)
	for _, sort := range []lq.EntrySort{lq.SortDate, lq.SortCategory, lq.SortClient, lq.SortHours, lq.SortOnSite} {
		for _, dir := range []lq.SortDirection{lq.Asc, lq.Desc} {
			whole := lf.list(t, sort, dir, lf.params())
			var paged []string
			for offset := int32(0); ; offset += 3 {
				p := lf.params()
				p.PageSize, p.PageOffset = 3, offset
				page := lf.list(t, sort, dir, p)
				paged = append(paged, page...)
				if len(page) < 3 {
					break
				}
			}
			if !slices.Equal(paged, whole) {
				t.Errorf("%s %s: pages of 3 = %v, one page = %v", sort, dir, paged, whole)
			}
		}
	}
}

func TestListEntries_PaginationIsStableWhenEverySortKeyTies(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	// Twenty-five legacy entries with the same date, category, hours and flag, and no clients: every
	// sort key ties, so only the final unique key can keep the pages apart.
	seen := map[pgtype.UUID]bool{}
	for range 25 {
		seen[f.entry(t, a, spec{date: "2026-03-02", minutes: 30, category: 1}).ID] = true
	}
	for _, sort := range []lq.EntrySort{lq.SortDate, lq.SortCategory, lq.SortClient, lq.SortHours, lq.SortOnSite} {
		for _, dir := range []lq.SortDirection{lq.Asc, lq.Desc} {
			whole, err := f.q.ListEntries(f.ctx, sort, dir, lq.ListEntriesParams{UserID: a, PageSize: 100})
			if err != nil || len(whole) != 25 {
				t.Fatalf("%s %s: %d entries, %v", sort, dir, len(whole), err)
			}
			got := map[pgtype.UUID]bool{}
			var order []pgtype.UUID
			for offset := int32(0); offset < 25; offset += 7 {
				page, err := f.q.ListEntries(f.ctx, sort, dir, lq.ListEntriesParams{UserID: a, PageSize: 7, PageOffset: offset})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range page {
					if got[e.ID] {
						t.Fatalf("%s %s: entry repeated across pages", sort, dir)
					}
					got[e.ID] = true
					order = append(order, e.ID)
				}
			}
			if len(got) != 25 {
				t.Errorf("%s %s: pages hold %d distinct entries, want 25", sort, dir, len(got))
			}
			for i, e := range whole {
				if order[i] != e.ID {
					t.Fatalf("%s %s: page order differs from the single-page order at %d", sort, dir, i)
				}
			}
		}
	}
}

func TestListEntries_ClientTextMatchesWildcardsLiterally(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	label := map[pgtype.UUID]string{}
	for i, name := range []string{`A%B`, `A_B`, `AxB`, `A\B`, `plain`} {
		label[f.entry(t, a, spec{
			date: "2026-03-02", start: fmt.Sprintf("%02d:00", 8+i), end: fmt.Sprintf("%02d:15", 8+i), minutes: 15, category: 1,
			clients: []lq.Client{f.client(t, a, name)},
		}).ID] = name
	}
	search := func(term string) []string {
		got, err := f.q.ListEntries(f.ctx, lq.SortDate, lq.Asc, lq.ListEntriesParams{UserID: a, PageSize: 50, ClientText: text(term)})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		return labels(got, label)
	}
	for term, want := range map[string][]string{
		`%`: {`A%B`}, `_`: {`A_B`}, `\`: {`A\B`}, `a%b`: {`A%B`}, `A_B`: {`A_B`}, `a\b`: {`A\B`},
		`A`: {`A%B`, `A_B`, `AxB`, `A\B`, `plain`}, `A%`: {`A%B`}, `%%`: nil, `__`: nil, `\\`: nil, `\%`: nil,
	} {
		if got := search(term); !slices.Equal(got, want) {
			t.Errorf("client text %q matched %v, want %v", term, got, want)
		}
	}
}

func TestListEntries_InjectionPayloadsAreOnlyData(t *testing.T) {
	f := newFixture(t)
	a := f.user(t, "pseudonym-a@example.test")
	f.entry(t, a, spec{date: "2026-03-02", start: "09:00", end: "10:00", minutes: 60, category: 1, clients: []lq.Client{f.client(t, a, "Alpha")}})
	payloads := []string{
		`' OR 1=1 --`, `'; DROP TABLE entries; --`, `" OR ""="`, `'); DELETE FROM clients; --`,
		`\'; SELECT pg_sleep(5); --`, `%' OR '1'='1`, `' UNION SELECT * FROM users --`, "\x00", "a b", strings.Repeat("%", 64),
	}
	for _, payload := range payloads {
		for _, sort := range []lq.EntrySort{lq.SortDate, lq.SortCategory, lq.SortClient, lq.SortHours, lq.SortOnSite} {
			got, err := f.q.ListEntries(f.ctx, sort, lq.Asc, lq.ListEntriesParams{
				UserID: a, PageSize: 50, ClientText: text(payload), CategoryGroup: text(payload),
			})
			if err != nil && !strings.Contains(err.Error(), "invalid byte sequence") {
				t.Fatalf("payload %q: %v", payload, err)
			}
			if len(got) != 0 {
				t.Errorf("payload %q matched %d entries", payload, len(got))
			}
		}
		// The payload as a client name is stored and found verbatim, never executed.
		if payload != "\x00" {
			name := payload
			if len(name) > 64 {
				name = name[:64]
			}
			c, err := f.q.InsertClient(f.ctx, lq.InsertClientParams{UserID: a, Name: name})
			if err != nil {
				t.Fatalf("store payload as client name %q: %v", payload, err)
			}
			if got, err := f.q.GetClientByName(f.ctx, lq.GetClientByNameParams{UserID: a, Name: name}); err != nil || got.ID != c.ID || got.Name != name {
				t.Fatalf("payload %q round trip = %+v, %v", payload, got, err)
			}
		}
	}
	for table, count := range map[string]string{
		"entries":    `SELECT count(*) FROM entries`,
		"clients":    `SELECT count(*) FROM clients`,
		"users":      `SELECT count(*) FROM users`,
		"categories": `SELECT count(*) FROM categories`,
	} {
		var n int
		if err := f.conn.QueryRow(f.ctx, count).Scan(&n); err != nil || n == 0 {
			t.Fatalf("table %s after the payloads: %d rows, %v", table, n, err)
		}
	}
}

func TestListEntries_NeverReturnsAnotherUsersEntries(t *testing.T) {
	lf := newListFixture(t)
	b := lf.user(t, "pseudonym-b@example.test")
	stranger := lf.entry(t, b, spec{date: "2026-03-03", start: "09:00", end: "10:00", minutes: 60, category: 1, clients: []lq.Client{lf.client(t, b, "alpha")}})

	for _, sort := range []lq.EntrySort{lq.SortDate, lq.SortCategory, lq.SortClient, lq.SortHours, lq.SortOnSite} {
		for _, dir := range []lq.SortDirection{lq.Asc, lq.Desc} {
			got := lf.list(t, sort, dir, lf.params())
			if len(got) != 7 || slices.Contains(got, "") {
				t.Errorf("%s %s listed %v for user A", sort, dir, got)
			}
		}
	}
	// Filters that match user B's data find nothing for user A, and the count agrees.
	p := lf.params()
	p.ClientText = text("alpha")
	for _, label := range lf.list(t, lq.SortDate, lq.Desc, p) {
		if label == "" {
			t.Errorf("another user's entry leaked into the client search")
		}
	}
	rows, err := lf.q.ListEntryClients(lf.ctx, lq.ListEntryClientsParams{UserID: lf.owner, EntryIds: []pgtype.UUID{stranger.ID}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("client lines of another user's entry = %v, %v", rows, err)
	}
	if n, err := lf.q.CountEntries(lf.ctx, lq.CountEntriesParams{UserID: lf.owner}); err != nil || n != 7 {
		t.Fatalf("count = %d, %v; want 7", n, err)
	}
}
