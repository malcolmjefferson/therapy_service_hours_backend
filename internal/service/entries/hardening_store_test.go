package entries_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// S2-16 end-to-end tests: concurrency and injection hardening through the real router, service and
// PostgreSQL store against the real migrations (GO-17, GO-72, SEC-22). Users, clients and notes are
// fictional pseudonyms (CLAUDE.md §0.7).

// injectionPayloads are the strings CLAUDE.md §11.1 requires against every endpoint that takes text,
// search or sort input: quotes, comments, statement terminators, backslashes and LIKE wildcards.
var injectionPayloads = []string{
	`' OR 1=1 --`,
	`'; DROP TABLE entries; --`,
	`"; DROP TABLE users; --`,
	`'; DROP TABLE clients; --`,
	`\`,
	`%`,
	`_`,
	`' || (SELECT 1) || '`,
	`$1`,
	"line one\nline two\ttabbed",
}

// logHoursTables are the tables a payload must never be able to damage.
var logHoursTables = []string{"users", "organizations", "categories", "clients", "entries", "entry_clients"}

// rowCounts returns the number of rows in every log-hours table, failing when a table is gone.
func (e *env) rowCounts() map[string]int {
	e.t.Helper()
	counts := make(map[string]int, len(logHoursTables))
	for _, table := range logHoursTables {
		var exists bool
		if err := e.conn.QueryRow(e.t.Context(), `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil || !exists {
			e.t.Fatalf("table %s is missing after the payloads (err %v)", table, err)
		}
		// The table name comes from the fixed list above and the count query cannot take an identifier
		// parameter; this is test-only SQL (SEC-22 applies to application code).
		// nosemgrep: no-string-built-sql
		counts[table] = e.count("SELECT count(*) FROM " + table)
	}
	return counts
}

// assertNoSQLLeak fails when a response shows database internals or echoes the payload back.
func assertNoSQLLeak(t *testing.T, what, payload, body string) {
	t.Helper()
	for _, leak := range []string{"SQLSTATE", "syntax error", "pgx", "pq:", "relation \"", "DROP TABLE"} {
		if strings.Contains(body, leak) {
			t.Errorf("%s with %q leaked %q: %s", what, payload, leak, body)
		}
	}
}

func TestConcurrentMovesIntoOneWindow_ExactlyOneEntryWins(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	const n = 8
	ids := make([]string, n)
	for i := range n {
		// Eight separate one-hour sessions that overlap nothing, each about to be moved to 14:00.
		ids[i] = e.entry(a, row{date: "2026-10-05", start: fmt.Sprintf("%02d:00", 6+i), end: fmt.Sprintf("%02d:00", 7+i)})
	}
	e.actor = a
	// Bodies are built first because the test's own connection is not safe for concurrent use.
	body := e.body("2026-10-05", "14:00", "15:00", 1)
	statuses := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i], bodies[i] = e.call(http.MethodPut, "/entries/"+ids[i], body)
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, status := range statuses {
		if status == http.StatusOK {
			winners++
			continue
		}
		expectProblem(t, status, bodies[i], http.StatusConflict, "entry-overlap")
	}
	if winners != 1 {
		t.Fatalf("%d moves succeeded, want exactly 1: %v", winners, statuses)
	}
	if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND start_time = '14:00'`, a.ID); got != 1 {
		t.Errorf("%d entries start at 14:00, want 1", got)
	}
	// Whatever the interleaving, no two of the user's entries may share a minute.
	if got := e.count(`
SELECT count(*) FROM entries x JOIN entries y
  ON x.user_id = y.user_id AND x.session_date = y.session_date AND x.id < y.id
 AND x.start_time < y.end_time AND y.start_time < x.end_time`); got != 0 {
		t.Errorf("%d pairs of overlapping entries were stored", got)
	}
	if got := e.count(`SELECT count(*) FROM entries`); got != n {
		t.Errorf("%d entries, want the original %d", got, n)
	}
}

func TestConcurrentCreates_OverlapIsPerUserNotGlobal(t *testing.T) {
	e := newEnv(t)
	const n = 6
	bodies := make([]string, n)
	actors := make([]domain.Actor, n)
	for i := range n {
		actors[i] = e.user(fmt.Sprintf("user%d@example.test", i))
		bodies[i] = e.body("2026-10-05", "09:00", "10:00", 1)
	}
	// The same window for six different users at once: none of them clashes with the others.
	statuses := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i], _ = e.callAs(actors[i], http.MethodPost, "/entries", bodies[i])
		}()
	}
	close(start)
	wg.Wait()
	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Errorf("user %d got %d, want 201: another user's session must not block this one", i, status)
		}
	}
	if got := e.count(`SELECT count(*) FROM entries`); got != n {
		t.Errorf("%d entries stored, want %d", got, n)
	}
}

func TestInjectionPayloads_TextFieldsAreStoredAndMatchedLiterally(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	bystander := e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00", notes: "untouched"})
	bystanderClient := e.client(b, "Bystander")
	e.actor = a

	clientsMade := 0
	for i, p := range injectionPayloads {
		t.Run(fmt.Sprintf("payload %d", i), func(t *testing.T) {
			e.notesAreStoredLiterally(t, i, p)
			clientID := e.clientNameIsStoredLiterally(t, p)
			if clientID == "" {
				return
			}
			clientsMade++
			e.clientFilterMatchesLiterally(t, i, p, clientID, bystander)
		})
	}

	// Each payload made one entry with notes, and each client made one more to find it by.
	counts := e.rowCounts()
	if want := 1 + len(injectionPayloads) + clientsMade; counts["entries"] != want {
		t.Errorf("%d entries after the payloads, want %d (%v)", counts["entries"], want, counts)
	}
	if want := 1 + clientsMade; counts["clients"] != want {
		t.Errorf("%d clients after the payloads, want %d (%v)", counts["clients"], want, counts)
	}
	var notes, name string
	if err := e.conn.QueryRow(t.Context(), `SELECT notes FROM entries WHERE id = $1`, bystander).Scan(&notes); err != nil || notes != "untouched" {
		t.Errorf("another user's entry changed: %q (%v)", notes, err)
	}
	if err := e.conn.QueryRow(t.Context(), `SELECT name FROM clients WHERE id = $1`, bystanderClient).Scan(&name); err != nil || name != "Bystander" {
		t.Errorf("another user's client changed: %q (%v)", name, err)
	}
}

// notesAreStoredLiterally creates an entry with the payload as its notes, then replaces the notes
// with it again through PUT; both responses must return it byte for byte.
func (e *env) notesAreStoredLiterally(t *testing.T, i int, payload string) {
	t.Helper()
	start, end := fmt.Sprintf("%02d:00", 6+i), fmt.Sprintf("%02d:30", 6+i)
	notes := `"notes":` + jsonString(t, payload)
	created := e.created(e.body("2026-10-05", start, end, 1, notes))
	if created["notes"] != payload {
		t.Errorf("created notes = %q, want %q", created["notes"], payload)
	}
	status, res := e.call(http.MethodPut, "/entries/"+created["id"].(string), e.body("2026-10-05", start, end, 2, notes))
	if status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", status, res)
	}
	if got := decode[map[string]any](t, res)["notes"]; got != payload {
		t.Errorf("updated notes = %q, want %q", got, payload)
	}
}

// clientNameIsStoredLiterally creates and renames a client using the payload and returns its id.
// Names may not hold control characters, so for those the payload is a clean 400 and the id is "".
func (e *env) clientNameIsStoredLiterally(t *testing.T, payload string) string {
	t.Helper()
	status, res := e.call(http.MethodPost, "/clients", `{"name":`+jsonString(t, payload)+`}`)
	if strings.ContainsAny(payload, "\n\t") {
		expectProblem(t, status, res, http.StatusBadRequest, "validation")
		return ""
	}
	if status != http.StatusCreated {
		t.Fatalf("POST /clients = %d: %s", status, res)
	}
	client := decode[map[string]any](t, res)
	if client["name"] != payload {
		t.Errorf("client name = %q, want %q", client["name"], payload)
	}
	id := client["id"].(string)
	renamed := payload + " (renamed)"
	status, res = e.call(http.MethodPatch, "/clients/"+id, `{"name":`+jsonString(t, renamed)+`}`)
	if status != http.StatusOK || decode[map[string]any](t, res)["name"] != renamed {
		t.Errorf("PATCH /clients = %d: %s", status, res)
	}
	return id
}

// clientFilterMatchesLiterally logs an entry for the client and filters by the payload: the filter
// treats it as text, so it finds that entry and never another user's, a quote does not widen the
// match and a wildcard is not a wildcard.
func (e *env) clientFilterMatchesLiterally(t *testing.T, i int, payload, clientID, bystander string) {
	t.Helper()
	start, end := fmt.Sprintf("%02d:30", 6+i), fmt.Sprintf("%02d:45", 6+i)
	withClient := e.created(e.body("2026-10-05", start, end, 1, clientIDs(clientID)))["id"].(string)
	got := e.list(q("client", payload)).ids()
	if !containsID(got, withClient) {
		t.Errorf("client=%q did not find the entry of the client with that name", payload)
	}
	if containsID(got, bystander) {
		t.Errorf("client=%q returned another user's entry", payload)
	}
}

func TestInjectionPayloads_IdentifiersAndFiltersAreRefusedCleanly(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	mine := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", notes: "keep"})
	myClient := e.client(a, "Keep")
	e.actor = a
	before := e.rowCounts()
	valid := e.body("2026-10-05", "11:00", "12:00", 1)

	for _, p := range injectionPayloads {
		quoted := jsonString(t, p)
		requests := map[string]struct{ method, path, body string }{
			"category_id":    {http.MethodPost, "/entries", fmt.Sprintf(`{"session_date":"2026-10-05","start_time":"11:00","end_time":"12:00","category_id":%s}`, quoted)},
			"session_date":   {http.MethodPost, "/entries", fmt.Sprintf(`{"session_date":%s,"start_time":"11:00","end_time":"12:00","category_id":%q}`, quoted, e.category(1))},
			"start_time":     {http.MethodPost, "/entries", fmt.Sprintf(`{"session_date":"2026-10-05","start_time":%s,"end_time":"12:00","category_id":%q}`, quoted, e.category(1))},
			"client_ids":     {http.MethodPost, "/entries", strings.TrimSuffix(valid, "}") + `,"client_ids":[` + quoted + `]}`},
			"update ids":     {http.MethodPut, "/entries/" + mine, fmt.Sprintf(`{"session_date":"2026-10-05","start_time":"11:00","end_time":"12:00","category_id":%s}`, quoted)},
			"client name":    {http.MethodPost, "/clients", `{"name":` + quoted + `,"user_id":` + quoted + `}`},
			"path entry id":  {http.MethodGet, "/entries/" + url.PathEscape(p), ""},
			"path update id": {http.MethodPut, "/entries/" + url.PathEscape(p), valid},
			"path delete id": {http.MethodDelete, "/entries/" + url.PathEscape(p), ""},
			"path client id": {http.MethodPatch, "/clients/" + url.PathEscape(p), `{"name":"x"}`},
			"path remove id": {http.MethodDelete, "/clients/" + url.PathEscape(p), ""},
		}
		for name, r := range requests {
			status, body := e.call(r.method, r.path, r.body)
			// Refused cleanly: a client error with a problem body, never a server error.
			if status < 400 || status >= 500 {
				t.Errorf("%s with %q: status %d, want a 4xx: %s", name, p, status, body)
			}
			assertNoSQLLeak(t, name, p, body)
		}
		for _, param := range []string{"sort", "dir", "group", "from", "to", "page", "page_size"} {
			status, body := e.get(q(param, p))
			if status != http.StatusBadRequest {
				t.Errorf("%s=%q: status %d, want 400: %s", param, p, status, body)
			}
			assertNoSQLLeak(t, param, p, body)
		}
	}

	after := e.rowCounts()
	for table, n := range before {
		if after[table] != n {
			t.Errorf("table %s has %d rows after refused payloads, want %d", table, after[table], n)
		}
	}
	var notes, name string
	if err := e.conn.QueryRow(t.Context(), `SELECT notes FROM entries WHERE id = $1`, mine).Scan(&notes); err != nil || notes != "keep" {
		t.Errorf("the entry changed: %q (%v)", notes, err)
	}
	if err := e.conn.QueryRow(t.Context(), `SELECT name FROM clients WHERE id = $1`, myClient).Scan(&name); err != nil || name != "Keep" {
		t.Errorf("the client changed: %q (%v)", name, err)
	}
}

func TestClientsAndEntries_AnotherUsersRecordsAreInvisibleAndUntouchable(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	theirClient := e.client(b, "Theirs")
	theirEntry := e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00", clients: []string{theirClient}})
	e.actor = a

	if status, body := e.call(http.MethodPatch, "/clients/"+theirClient, `{"name":"Taken over"}`); status != http.StatusNotFound {
		t.Errorf("PATCH another user's client = %d, want 404: %s", status, body)
	}
	if status, body := e.call(http.MethodDelete, "/clients/"+theirClient+"?confirm_detach=true", ""); status != http.StatusNotFound {
		t.Errorf("DELETE another user's client = %d, want 404: %s", status, body)
	}
	if status, body := e.call(http.MethodGet, "/clients", ""); status != http.StatusOK || strings.Contains(body, "Theirs") {
		t.Errorf("GET /clients = %d showed another user's client: %s", status, body)
	}
	for _, c := range []struct{ method, body string }{{http.MethodGet, ""}, {http.MethodPut, e.body("2026-10-05", "09:00", "10:00", 1)}, {http.MethodDelete, ""}} {
		if status, body := e.call(c.method, "/entries/"+theirEntry, c.body); status != http.StatusNotFound {
			t.Errorf("%s another user's entry = %d, want 404: %s", c.method, status, body)
		}
	}
	if got := e.list(nil); len(got.Items) != 0 {
		t.Errorf("GET /entries showed %d of another user's entries", len(got.Items))
	}
	// A is not told which of B's records exist: an entry naming B's client is a plain 400 and stores nothing.
	status, body := e.call(http.MethodPost, "/entries", e.body("2026-10-06", "09:00", "10:00", 1, clientIDs(theirClient)))
	expectProblem(t, status, body, http.StatusBadRequest, "validation")
	var name string
	if err := e.conn.QueryRow(t.Context(), `SELECT name FROM clients WHERE id = $1`, theirClient).Scan(&name); err != nil || name != "Theirs" {
		t.Errorf("another user's client was changed: %q (%v)", name, err)
	}
	if got := e.count(`SELECT count(*) FROM entries WHERE id = $1`, theirEntry); got != 1 {
		t.Error("another user's entry was deleted")
	}
	if got := e.count(`SELECT count(*) FROM entry_clients WHERE entry_id = $1`, theirEntry); got != 1 {
		t.Error("another user's client link was detached")
	}
}

func containsID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

// jsonString encodes s as a JSON string literal.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
