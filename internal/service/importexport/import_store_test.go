package importexport_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

// S4-03 integration tests: the import service over the real PostgreSQL store and migrations
// (GO-17, GO-72). Users, clients and notes are fictional pseudonyms (CLAUDE.md §0.7).

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

type env struct {
	t       *testing.T
	pgName  string
	conn    *pgx.Conn
	pool    *pgxpool.Pool
	store   *db.ImportStore
	service *importexport.Service
}

// openPool opens a pool like the API Lambda's (db.MaxConns connections) on the test database.
func openPool(t *testing.T, database string) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(t.Context(), db.Settings{
		Host: server.Host, Port: server.Port, Database: database, User: server.User,
		Mode: db.AuthPassword, Password: server.Password,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	database := server.NewDatabase(t)
	pool := openPool(t, database)
	sqlDB := stdlib.OpenDBFromPool(pool)
	res := migrate.Run(t.Context(), sqlDB, migrations.FS, migrate.Options{})
	_ = sqlDB.Close()
	if res.Status != migrate.StatusOK {
		t.Fatalf("migrate: %+v", res)
	}
	store := db.NewImportStore(pool)
	service, err := importexport.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, pgName: database, conn: server.Connect(t, database), pool: pool, store: store, service: service}
}

// user inserts an active user in the seeded organization.
func (e *env) user(email string) domain.Actor {
	e.t.Helper()
	var id, org string
	err := e.conn.QueryRow(e.t.Context(),
		`INSERT INTO users (organization_id, email) SELECT id, $1 FROM organizations WHERE name = 'Default Organization' RETURNING id::text, organization_id::text`,
		email).Scan(&id, &org)
	if err != nil {
		e.t.Fatalf("insert user: %v", err)
	}
	return domain.Actor{ID: id, OrganizationID: org, Email: email, Active: true}
}

func (e *env) importAs(a domain.Actor, text string) importexport.Report {
	e.t.Helper()
	report, err := e.service.Import(e.t.Context(), a, strings.NewReader(text))
	if err != nil {
		e.t.Fatalf("Import() error = %v", err)
	}
	return report
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.t.Context(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func (e *env) entryCount(a domain.Actor) int {
	return e.count(`SELECT count(*) FROM entries WHERE user_id = $1`, a.ID)
}

func (e *env) clientCount(a domain.Actor) int {
	return e.count(`SELECT count(*) FROM clients WHERE user_id = $1`, a.ID)
}

// insertTimed stores a timed entry directly, in the given status, in category "Individual Counseling".
func (e *env) insertTimed(a domain.Actor, date, start, end, status string) {
	e.t.Helper()
	e.insertTimedCtx(e.t.Context(), a, date, start, end, status)
}

func (e *env) insertTimedCtx(ctx context.Context, a domain.Actor, date, start, end, status string) {
	e.t.Helper()
	_, err := e.conn.Exec(ctx, `
		INSERT INTO entries (user_id, session_date, start_time, end_time, duration_minutes, category_id, status, needs_time)
		SELECT $1, $2::date, $3::time, $4::time, (extract(epoch FROM ($4::time - $3::time)) / 60)::int, c.id, $5, false
		FROM categories c WHERE c.name = 'Individual Counseling'`, a.ID, date, start, end, status)
	if err != nil {
		e.t.Fatalf("insert entry: %v", err)
	}
}

func skippedReasons(r importexport.Report) []string {
	var out []string
	for _, s := range r.SkippedRows {
		out = append(out, fmt.Sprintf("%d:%s", s.Row, s.Reason))
	}
	return out
}

func TestStore_ImportsThePrototypeLedger(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	data, err := os.ReadFile("testdata/prototype_no_times.md")
	if err != nil {
		t.Fatal(err)
	}
	report := e.importAs(me, string(data))

	if report.TotalRows != 5 || report.ImportedCount != 5 || report.NeedsTimeCount != 5 || len(report.SkippedRows) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if got := e.clientCount(me); got != 4 || len(report.MergedClients) != 4 {
		t.Fatalf("clients = %d, merged = %d, want 4 each (Pseudonym A, B, C, Tuesday Group; case-insensitive)", got, len(report.MergedClients))
	}
	// Every imported entry is pending, flagged Needs time, with no times and its hours as minutes.
	if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND status = 'pending' AND needs_time AND start_time IS NULL AND end_time IS NULL`, me.ID); got != 5 {
		t.Fatalf("%d entries are pending and need a time, want 5", got)
	}
	var minutes int
	if err := e.conn.QueryRow(t.Context(), `SELECT sum(duration_minutes) FROM entries WHERE user_id = $1`, me.ID).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if want := (90 + 60 + 15 + 60 + 75); minutes != want {
		t.Fatalf("total minutes = %d, want %d", minutes, want)
	}
	// "Coping skills | week 2" keeps its pipe, and the row with two clients has both.
	if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND notes = 'Coping skills | week 2'`, me.ID); got != 1 {
		t.Fatalf("pipe note stored %d times, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM entry_clients WHERE user_id = $1`, me.ID); got != 1+2+0+1+1 {
		t.Fatalf("%d entry client links, want 5", got)
	}
}

func TestStore_ReimportAddsNothing(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	for _, file := range []string{"testdata/prototype_no_times.md", "testdata/with_times.md"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		first := e.importAs(me, string(data))
		entries, clients := e.entryCount(me), e.clientCount(me)

		again := e.importAs(me, string(data))
		if again.ImportedCount != 0 || again.TotalRows != first.TotalRows || len(again.SkippedRows) != first.ImportedCount {
			t.Fatalf("%s: second report = %+v", file, again)
		}
		for _, s := range again.SkippedRows {
			if s.Reason != importexport.ReasonDuplicate {
				t.Fatalf("%s: row %d skipped as %s, want duplicate", file, s.Row, s.Reason)
			}
		}
		if e.entryCount(me) != entries || e.clientCount(me) != clients {
			t.Fatalf("%s: re-import changed the data", file)
		}
		for _, m := range again.MergedClients {
			if m.Created {
				t.Fatalf("%s: re-import created client %q", file, m.Client.Name)
			}
		}
	}
}

func TestStore_ReimportAfterAddingATime(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	text := legacyHeader + "| 2026-01-05 | Direct Service | Individual Counseling | — | 1.5 | Yes | Intake follow-up |\n"
	e.importAs(me, text)
	// What S4-05 does when the user adds the missing time.
	if _, err := e.conn.Exec(t.Context(), `UPDATE entries SET start_time = '09:00', end_time = '10:30', needs_time = false WHERE user_id = $1`, me.ID); err != nil {
		t.Fatal(err)
	}
	report := e.importAs(me, text)
	if report.ImportedCount != 0 || e.entryCount(me) != 1 {
		t.Fatalf("report = %+v, entries = %d", report, e.entryCount(me))
	}
}

func TestStore_TimedRowsObeyTheOverlapRules(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	e.insertTimed(me, "2026-02-02", "10:00", "11:00", "pending")
	e.insertTimed(me, "2026-02-03", "10:00", "11:00", "rejected")

	report := e.importAs(me, timedHeader+
		"| 2026-02-02 | 10:30 | 11:30 | Direct Service | Individual Counseling | — |  | No | overlaps the stored session |\n"+
		"| 2026-02-02 | 11:00 | 12:00 | Direct Service | Individual Counseling | — |  | No | starts when it ends |\n"+
		"| 2026-02-02 | 11:45 | 12:15 | Direct Service | Individual Counseling | — |  | No | overlaps the previous row |\n"+
		"| 2026-02-03 | 10:00 | 11:00 | Direct Service | Individual Counseling | — |  | No | the stored one was rejected |\n"+
		"| 2026-02-04 |  |  | Direct Service | Individual Counseling | — | 1 | No | needs a time |\n"+
		"| 2026-02-04 | 09:00 | 10:00 | Direct Service | Individual Counseling | — |  | No | legacy rows never clash |\n")

	if got, want := strings.Join(skippedReasons(report), ","), "1:overlap,3:overlap"; got != want {
		t.Fatalf("skipped = %s, want %s; report = %+v", got, want, report)
	}
	if report.ImportedCount != 4 || report.NeedsTimeCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	// The stored sessions are untouched and the database constraint still holds with the new ones.
	if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND status = 'pending' AND start_time IS NOT NULL`, me.ID); got != 4 {
		t.Fatalf("%d pending timed entries, want 4 (1 stored + 3 imported)", got)
	}
}

func TestStore_OtherUsersDataIsNeverTouched(t *testing.T) {
	e := newEnv(t)
	me, other := e.user("trainee@example.test"), e.user("other@example.test")
	text := legacyHeader + "| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | note |\n"
	e.importAs(other, text)
	e.insertTimed(other, "2026-02-02", "10:00", "11:00", "pending")

	// The same file is not a duplicate for another user, and their client list is separate.
	report := e.importAs(me, text)
	if report.ImportedCount != 1 || !report.MergedClients[0].Created {
		t.Fatalf("report = %+v", report)
	}
	// A timed row for a time the other user has busy is not an overlap for this user.
	report = e.importAs(me, timedHeader+"| 2026-02-02 | 10:00 | 11:00 | Direct Service | Individual Counseling | — |  | No |  |\n")
	if report.ImportedCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	if e.entryCount(other) != 2 || e.clientCount(other) != 1 || e.entryCount(me) != 2 || e.clientCount(me) != 1 {
		t.Fatal("an import changed another user's data or mixed clients between users")
	}
	var shared int
	if err := e.conn.QueryRow(t.Context(), `
		SELECT count(*) FROM entry_clients ec JOIN clients c ON c.id = ec.client_id WHERE ec.user_id <> c.user_id`).Scan(&shared); err != nil || shared != 0 {
		t.Fatalf("entries use another user's clients: %d, %v", shared, err)
	}
}

func TestStore_MergesWithSavedClientsIgnoringCase(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	var savedID string
	if err := e.conn.QueryRow(t.Context(), `INSERT INTO clients (user_id, name) VALUES ($1, 'pseudonym a') RETURNING id::text`, me.ID).Scan(&savedID); err != nil {
		t.Fatal(err)
	}
	report := e.importAs(me, "## Clients / Groups\n- Pseudonym B\n- PSEUDONYM A\n\n## Entries\n"+legacyHeader+
		"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A; pseudonym b | 1 | No |  |\n")

	if len(report.MergedClients) != 2 || e.clientCount(me) != 2 {
		t.Fatalf("report = %+v, clients = %d", report, e.clientCount(me))
	}
	a, b := report.MergedClients[0], report.MergedClients[1]
	if a.Client.ID != savedID || a.Client.Name != "pseudonym a" || a.Created || b.Client.Name != "Pseudonym B" || !b.Created {
		t.Fatalf("merged = %+v", report.MergedClients)
	}
	if got := e.count(`SELECT count(*) FROM entry_clients WHERE user_id = $1 AND client_id = $2`, me.ID, savedID); got != 1 {
		t.Fatalf("saved client is on %d entries, want 1", got)
	}
}

// Injection-like cells are stored as the plain text they are, and nothing else runs (SEC-20).
func TestStore_InjectionLikeCellsAreStoredAsText(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	payloads := []string{
		`' OR 1=1 --`, `'; DROP TABLE entries; --`, `"; DELETE FROM users; --`, `\'; SELECT pg_sleep(5); --`,
		`100% _wild_ \ back`, `=HYPERLINK("http://example.test")`, `@SUM(1+1)`, `+cmd|' /C calc'!A0`, `<img src=x onerror=alert(1)>`,
	}
	var b strings.Builder
	b.WriteString(legacyHeader)
	for i, p := range payloads {
		cell := strings.ReplaceAll(p, "|", `\|`)
		fmt.Fprintf(&b, "| 2026-03-%02d | Direct Service | Individual Counseling | %s | 1 | No | %s |\n", i+1, strings.ReplaceAll(cell, ";", ","), cell)
	}
	report := e.importAs(me, b.String())
	if report.ImportedCount != len(payloads) {
		t.Fatalf("report = %+v", report)
	}
	for _, p := range payloads {
		if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND notes = $2`, me.ID, p); got != 1 {
			t.Errorf("notes %q stored %d times, want 1 unchanged", p, got)
		}
	}
	if e.count(`SELECT count(*) FROM users`) != 1 || e.count(`SELECT count(*) FROM categories`) != 12 {
		t.Fatal("an injected statement changed other tables")
	}
}

// A failure anywhere rolls back the clients and entries stored so far (one transaction).
func TestStore_FailureRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	boom := errors.New("boom")
	err := e.store.Import(t.Context(), me.ID, func(ctx context.Context, tx importexport.Tx) error {
		m, err := tx.MergeClient(ctx, "Pseudonym A")
		if err != nil {
			return err
		}
		cats, err := tx.Categories(ctx)
		if err != nil {
			return err
		}
		if err := tx.CreateEntry(ctx, importexport.NewEntry{
			SessionDate: day("2026-01-05"), DurationMinutes: 60, CategoryID: cats[0].ID, ClientIDs: []string{m.Client.ID},
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Import() error = %v, want boom", err)
	}
	if e.entryCount(me) != 0 || e.clientCount(me) != 0 {
		t.Fatal("a failed import left data behind")
	}
}

// A session another request stored after the import read the day makes the database refuse it. The
// whole import fails with a conflict and stores nothing, so the user can simply run it again.
func TestStore_ConcurrentOverlapFailsTheWholeImport(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	err := e.store.Import(t.Context(), me.ID, func(ctx context.Context, tx importexport.Tx) error {
		cats, err := tx.Categories(ctx)
		if err != nil {
			return err
		}
		// Committed from another connection after the import began.
		e.insertTimedCtx(ctx, me, "2026-02-02", "10:00", "11:00", "pending")
		return tx.CreateEntry(ctx, importexport.NewEntry{
			SessionDate: day("2026-02-02"), HasTimes: true, StartMinute: 630, EndMinute: 690, DurationMinutes: 60, CategoryID: cats[0].ID,
		})
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Import() error = %v, want ErrConflict", err)
	}
	if e.entryCount(me) != 1 {
		t.Fatalf("%d entries, want only the concurrent one", e.entryCount(me))
	}
}

// Several imports of the same file at once store it once: the user's imports run one after another.
func TestStore_ConcurrentImportsOfOneFileImportItOnce(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	data, err := os.ReadFile("testdata/prototype_no_times.md")
	if err != nil {
		t.Fatal(err)
	}
	const runs = 6
	reports := make([]importexport.Report, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.service.Import(context.WithoutCancel(t.Context()), me, strings.NewReader(string(data)))
			if err != nil {
				t.Errorf("import %d: %v", i, err)
			}
			reports[i] = r
		}()
	}
	wg.Wait()

	imported := 0
	for _, r := range reports {
		imported += r.ImportedCount
	}
	if imported != 5 || e.entryCount(me) != 5 || e.clientCount(me) != 4 {
		t.Fatalf("imported %d, stored %d entries and %d clients; want 5, 5 and 4", imported, e.entryCount(me), e.clientCount(me))
	}
}

// The store holds a lock per user: a second import of the same user does not start until the first
// has committed, while another user's import is not held up. Without the lock two imports of one file
// could both find no duplicates.
func TestStore_ImportsOfOneUserRunOneAfterAnother(t *testing.T) {
	e := newEnv(t)
	me, other := e.user("trainee@example.test"), e.user("other@example.test")
	ctx := context.WithoutCancel(t.Context())
	// The pool of one Lambda instance has two connections, which the two imports of one user use up,
	// so the other user's import goes through a pool of its own.
	otherStore := db.NewImportStore(openPool(t, e.pgName))

	firstInside, releaseFirst, secondInside := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	// A failed assertion must not leave the first import holding its connection, or the pool cannot close.
	t.Cleanup(func() { release.Do(func() { close(releaseFirst) }) })
	var wg sync.WaitGroup
	run := func(store *db.ImportStore, userID string, started chan<- struct{}, hold <-chan struct{}) {
		defer wg.Done()
		err := store.Import(ctx, userID, func(context.Context, importexport.Tx) error {
			close(started)
			if hold != nil {
				<-hold
			}
			return nil
		})
		if err != nil {
			t.Errorf("import: %v", err)
		}
	}
	wg.Add(1)
	go run(e.store, me.ID, firstInside, releaseFirst)
	<-firstInside

	wg.Add(1)
	go run(e.store, me.ID, secondInside, nil)
	select {
	case <-secondInside:
		t.Fatal("a second import of the same user started while the first was open")
	case <-time.After(300 * time.Millisecond):
	}

	otherInside := make(chan struct{})
	wg.Add(1)
	go run(otherStore, other.ID, otherInside, nil)
	select {
	case <-otherInside:
	case <-time.After(10 * time.Second):
		t.Fatal("another user's import was held up")
	}

	release.Do(func() { close(releaseFirst) })
	select {
	case <-secondInside:
	case <-time.After(10 * time.Second):
		t.Fatal("the second import never started after the first committed")
	}
	wg.Wait()
}

// The largest file the endpoint accepts: 5,000 rows in one transaction.
func TestStore_ImportsTheRowLimit(t *testing.T) {
	e := newEnv(t)
	me := e.user("trainee@example.test")
	var b strings.Builder
	b.WriteString(legacyHeader)
	for i := range importexport.MaxRows {
		fmt.Fprintf(&b, "| 2025-%02d-%02d | Direct Service | Individual Counseling | Pseudonym %d; Group %d | %d.25 | No | row %d |\n",
			i%12+1, i%28+1, i%40, i%7, i%8+1, i)
	}
	text := b.String()
	if len(text) > importexport.MaxFileBytes {
		t.Fatalf("test file is %d bytes, over the limit", len(text))
	}
	report := e.importAs(me, text)
	if report.TotalRows != importexport.MaxRows || report.ImportedCount != importexport.MaxRows || len(report.MergedClients) != 47 {
		t.Fatalf("report: total %d imported %d skipped %d clients %d", report.TotalRows, report.ImportedCount, len(report.SkippedRows), len(report.MergedClients))
	}
	again := e.importAs(me, text)
	if again.ImportedCount != 0 || len(again.SkippedRows) != importexport.MaxRows {
		t.Fatalf("re-import: imported %d skipped %d", again.ImportedCount, len(again.SkippedRows))
	}
}
