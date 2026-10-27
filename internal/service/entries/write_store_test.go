package entries_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	httpapi "github.com/malcolmjefferson/therapy_service_hours_backend/internal/http"
)

// S2-05 end-to-end tests: POST, GET, PUT and DELETE through the real router, service and PostgreSQL
// store against the real migrations (GO-17, GO-72). Users, clients and notes are fictional pseudonyms
// (CLAUDE.md §0.7).

// call sends one request as e.actor and returns the status and body. Each POST carries a fresh
// Idempotency-Key, as the client does for each user action; callWithKey repeats one.
func (e *env) call(method, path, body string) (int, string) {
	e.t.Helper()
	return e.callWithKey(method, path, body, fmt.Sprintf("00000000-0000-4000-8000-%012d", e.keys.Add(1)))
}

// defaultSubject is the authorizer subject of requests made as e.actor.
const defaultSubject = "s"

// callWithKey is call with an explicit Idempotency-Key on POSTs.
func (e *env) callWithKey(method, path, body, key string) (int, string) {
	e.t.Helper()
	return e.send(defaultSubject, method, path, body, key)
}

// callAs is call as another user, for requests made at the same time by several users.
func (e *env) callAs(actor domain.Actor, method, path, body string) (int, string) {
	e.t.Helper()
	e.actors.Store(actor.ID, actor)
	return e.send(actor.ID, method, path, body, fmt.Sprintf("00000000-0000-4000-8000-%012d", e.keys.Add(1)))
}

// send makes the request with the given authorizer subject; a path may carry a query string.
func (e *env) send(subject, method, path, body, key string) (int, string) {
	e.t.Helper()
	path, query, _ := strings.Cut(path, "?")
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + path, RawQueryString: query, Body: body, Headers: map[string]string{"content-type": "application/json"}}
	if method == http.MethodPost {
		event.Headers["idempotency-key"] = key
	}
	event.RequestContext.HTTP.Method = method
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": subject}}}
	res, err := apigw.Handler(e.router, httpapi.ContextWithRequestID)(e.t.Context(), event)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.StatusCode, res.Body
}

// category returns the id of the category at display position n (CLAUDE.md §8).
func (e *env) category(n int) string {
	e.t.Helper()
	var id string
	if err := e.conn.QueryRow(e.t.Context(), `SELECT id::text FROM categories WHERE sort_order = $1`, n).Scan(&id); err != nil {
		e.t.Fatalf("category %d: %v", n, err)
	}
	return id
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.conn.QueryRow(e.t.Context(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

// body builds an entry request. extra fields are merged in as raw JSON, for example `"notes":"x"`.
func (e *env) body(date, start, end string, category int, extra ...string) string {
	parts := []string{
		fmt.Sprintf(`"session_date":%q`, date), fmt.Sprintf(`"start_time":%q`, start),
		fmt.Sprintf(`"end_time":%q`, end), fmt.Sprintf(`"category_id":%q`, e.category(category)),
	}
	return "{" + strings.Join(append(parts, extra...), ",") + "}"
}

func clientIDs(ids ...string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	return `"client_ids":[` + strings.Join(quoted, ",") + `]`
}

type problem struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Errors []struct {
		Field string `json:"field"`
	} `json:"errors"`
	ConflictingEntry map[string]any `json:"conflicting_entry"`
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func (e *env) created(body string) map[string]any {
	e.t.Helper()
	status, res := e.call(http.MethodPost, "/entries", body)
	if status != http.StatusCreated {
		e.t.Fatalf("POST /entries = %d: %s", status, res)
	}
	return decode[map[string]any](e.t, res)
}

// expectProblem asserts the status and problem type of a response.
func expectProblem(t *testing.T, status int, body string, wantStatus int, wantType string) problem {
	t.Helper()
	p := decode[problem](t, body)
	if status != wantStatus || p.Type != "urn:therapy-service-hours:problem:"+wantType {
		t.Fatalf("got %d %s, want %d %s (body %s)", status, p.Type, wantStatus, wantType, body)
	}
	return p
}

func TestCreateEntry_StoresAPendingEntryAndReturnsTheOwnerView(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	zed, alpha := e.client(a, "Zed"), e.client(a, "alpha")
	e.actor = a

	got := e.created(e.body("2026-10-05", "13:15", "14:30", 11, `"on_site":true`, `"notes":"  Pseudonym note  "`, clientIDs(zed, alpha)))
	want := map[string]any{
		"session_date": "2026-10-05", "start_time": "13:15", "end_time": "14:30", "duration_minutes": float64(75), "hours": 1.25,
		"on_site": true, "notes": "Pseudonym note", "status": "pending", "needs_time": false,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if cat := got["category"].(map[string]any); cat["name"] != "Group Supervision" || cat["group"] != "supervision" {
		t.Errorf("category = %v", cat)
	}
	var names []string
	for _, c := range got["clients"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"alpha", "Zed"}) {
		t.Errorf("clients = %v", names)
	}
	for _, absent := range []string{"review_note", "reviewed_at"} {
		if _, present := got[absent]; present {
			t.Errorf("a new entry has %q", absent)
		}
	}

	e.checkStored(got["id"].(string))
}

// checkStored asserts the stored row agrees with the response, and that GET and the list return the same entry.
func (e *env) checkStored(id string) {
	e.t.Helper()
	var minutes int
	var status string
	var needsTime bool
	if err := e.conn.QueryRow(e.t.Context(), `SELECT duration_minutes, status, needs_time FROM entries WHERE id = $1`, id).Scan(&minutes, &status, &needsTime); err != nil {
		e.t.Fatal(err)
	}
	if minutes != 75 || status != "pending" || needsTime {
		e.t.Errorf("stored duration=%d status=%s needs_time=%v", minutes, status, needsTime)
	}
	if code, body := e.call(http.MethodGet, "/entries/"+id, ""); code != http.StatusOK || decode[map[string]any](e.t, body)["id"] != id {
		e.t.Errorf("GET = %d %s", code, body)
	}
	if list := e.list(nil); list.Page.TotalItems != 1 || list.ids()[0] != id {
		e.t.Errorf("list = %v", list.ids())
	}
}

func TestCreateEntry_WithoutOptionalFieldsHasNoClientsAndNoNotes(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	got := e.created(e.body("2026-10-05", "09:00", "09:15", 1))
	if got["on_site"] != false || len(got["clients"].([]any)) != 0 {
		t.Errorf("entry = %v", got)
	}
	if _, present := got["notes"]; present {
		t.Errorf("notes = %v", got["notes"])
	}
}

func TestCreateEntry_RefusesInvalidInputAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.actor = a
	valid := e.body("2026-10-05", "09:00", "10:00", 1)
	tests := []struct {
		name, body string
		fields     []string
	}{
		{"client-sent hours", strings.TrimSuffix(valid, "}") + `,"hours":1}`, nil},
		{"client-sent duration", strings.TrimSuffix(valid, "}") + `,"duration_minutes":60}`, nil},
		{"end before start", e.body("2026-10-05", "10:00", "09:00", 1), []string{"end_time"}},
		{"crosses midnight", e.body("2026-10-05", "23:45", "00:00", 1), []string{"end_time"}},
		{"off the 15-minute step", e.body("2026-10-05", "09:10", "10:00", 1), []string{"start_time"}},
		{"impossible date", e.body("2026-02-30", "09:00", "10:00", 1), nil},
		{"all problems at once", e.body("2026-10-05", "09:10", "09:20", 1, `"notes":"bell\u0007"`), []string{"start_time", "end_time", "notes"}},
		{"unknown category", fmt.Sprintf(`{"session_date":"2026-10-05","start_time":"09:00","end_time":"10:00","category_id":%q}`, "00000000-0000-4000-8000-00000000dead"), []string{"category_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := e.call(http.MethodPost, "/entries", tt.body)
			p := expectProblem(t, status, body, http.StatusBadRequest, "validation")
			var fields []string
			for _, f := range p.Errors {
				fields = append(fields, f.Field)
			}
			if tt.fields != nil && !slices.Equal(fields, tt.fields) {
				t.Errorf("fields = %v, want %v", fields, tt.fields)
			}
		})
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 0 {
		t.Errorf("%d entries were stored by refused requests", n)
	}
}

func TestCreateEntry_ClientsMustBeTheCallersOwnAndNothingIsHalfSaved(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	mine, theirs := e.client(a, "Mine"), e.client(b, "Theirs")
	e.actor = a
	for name, ids := range map[string][]string{
		"another user's client": {theirs},
		"one own, one foreign":  {mine, theirs},
		"a client that is gone": {"00000000-0000-4000-8000-00000000dead"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := e.call(http.MethodPost, "/entries", e.body("2026-10-05", "09:00", "10:00", 1, clientIDs(ids...)))
			p := expectProblem(t, status, body, http.StatusBadRequest, "validation")
			if len(p.Errors) != 1 || p.Errors[0].Field != "client_ids" {
				t.Errorf("errors = %+v", p.Errors)
			}
			if strings.Contains(body, "Theirs") || strings.Contains(body, theirs) {
				t.Errorf("response reveals the other user's client: %s", body)
			}
		})
	}
	if n := e.count(`SELECT count(*) FROM entries`) + e.count(`SELECT count(*) FROM entry_clients`); n != 0 {
		t.Errorf("%d rows were left behind by refused requests", n)
	}
}

func TestCreateEntry_OverlapIs409NamingTheClashAndNothingIsStored(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00"})
	e.actor = a
	first := e.created(e.body("2026-10-05", "09:00", "10:00", 1))

	for name, window := range map[string][2]string{
		"identical":         {"09:00", "10:00"},
		"starts inside":     {"09:30", "10:30"},
		"ends inside":       {"08:30", "09:30"},
		"surrounds":         {"08:00", "11:00"},
		"inside":            {"09:15", "09:45"},
		"one quarter hour ": {"09:45", "10:15"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := e.call(http.MethodPost, "/entries", e.body("2026-10-05", window[0], window[1], 7))
			p := expectProblem(t, status, body, http.StatusConflict, "entry-overlap")
			want := map[string]any{"id": first["id"], "session_date": "2026-10-05", "start_time": "09:00", "end_time": "10:00"}
			if fmt.Sprint(p.ConflictingEntry) != fmt.Sprint(want) {
				t.Errorf("conflicting_entry = %v, want %v", p.ConflictingEntry, want)
			}
		})
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE user_id = $1`, a.ID); n != 1 {
		t.Errorf("A has %d entries, want 1", n)
	}
}

func TestCreateEntry_SessionsThatDoNotOverlapAreAllowed(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00"})
	e.entry(a, row{date: "2026-10-05", start: "13:00", end: "14:00", status: "rejected"})
	e.entry(a, row{date: "2026-10-05", minutes: 120}) // A legacy entry without times blocks nothing.
	e.actor = a
	for name, w := range map[string][4]string{
		"back to back after":       {"2026-10-05", "10:00", "11:00", "x"},
		"back to back before":      {"2026-10-05", "08:00", "09:00", "x"},
		"same time, other user":    {"2026-10-05", "09:00", "10:00", "x"},
		"same time, other day":     {"2026-10-06", "09:00", "10:00", "x"},
		"over a rejected entry":    {"2026-10-05", "13:00", "14:00", "x"},
		"over a legacy entry":      {"2026-10-05", "15:00", "16:00", "x"},
		"the last quarter hour":    {"2026-10-05", "23:30", "23:45", "x"},
		"the first quarter of day": {"2026-10-07", "00:00", "00:15", "x"},
	} {
		t.Run(name, func(*testing.T) { e.created(e.body(w[0], w[1], w[2], 1)) })
	}
}

func TestCreateEntry_ConcurrentOverlappingCreatesExactlyOneSucceeds(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.actor = a
	const n = 8
	type result struct {
		status int
		body   string
	}
	results := make([]result, n)
	// The bodies are built first because the test's own connection is not safe for concurrent use.
	bodies := make([]string, n)
	for i := range n {
		// Every request overlaps the others by at least 30 minutes.
		bodies[i] = e.body("2026-10-05", fmt.Sprintf("%02d:00", 9+i%2), fmt.Sprintf("%02d:30", 10+i%2), 1+i%3)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, body := e.call(http.MethodPost, "/entries", bodies[i])
			results[i] = result{status, body}
		}()
	}
	close(start)
	wg.Wait()

	var winner string
	for _, r := range results {
		if r.status == http.StatusCreated {
			if winner != "" {
				t.Fatalf("two creates succeeded: %v", results)
			}
			winner = decode[map[string]any](t, r.body)["id"].(string)
		}
	}
	if winner == "" {
		t.Fatalf("no create succeeded: %v", results)
	}
	for _, r := range results {
		if r.status == http.StatusCreated {
			continue
		}
		p := expectProblem(t, r.status, r.body, http.StatusConflict, "entry-overlap")
		if p.ConflictingEntry["id"] != winner {
			t.Errorf("conflict names %v, want the winner %s", p.ConflictingEntry["id"], winner)
		}
	}
	if got := e.count(`SELECT count(*) FROM entries WHERE user_id = $1`, a.ID); got != 1 {
		t.Errorf("%d entries stored, want exactly 1", got)
	}
}

func TestCreateEntry_StoresInjectionPayloadsAsText(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	payload := `'; DROP TABLE entries; -- ' OR 1=1 \ % _`
	notes, _ := json.Marshal(payload)
	got := e.created(e.body("2026-10-05", "09:00", "10:00", 1, `"notes":`+string(notes)))
	if got["notes"] != payload {
		t.Errorf("notes = %v", got["notes"])
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 1 {
		t.Errorf("entries = %d", n)
	}
}

func TestGetEntry_OtherUsersEntryLooksLikeAMissingOne(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	theirs := e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00", notes: "private pseudonym note"})
	e.actor = a
	status, foreign := e.call(http.MethodGet, "/entries/"+theirs, "")
	expectProblem(t, status, foreign, http.StatusNotFound, "not-found")
	status, missing := e.call(http.MethodGet, "/entries/00000000-0000-4000-8000-00000000dead", "")
	expectProblem(t, status, missing, http.StatusNotFound, "not-found")
	if strings.Contains(foreign, "private") {
		t.Errorf("404 leaks the entry: %s", foreign)
	}
	if strings.ReplaceAll(foreign, "request-id", "") != strings.ReplaceAll(missing, "request-id", "") {
		t.Errorf("a foreign entry and a missing one answer differently:\n%s\n%s", foreign, missing)
	}
}

func TestUpdateEntry_ReplacesEveryFieldAndTheClientSet(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	one, two, three := e.client(a, "One"), e.client(a, "Two"), e.client(a, "Three")
	id := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", category: 1, onSite: true, notes: "old", clients: []string{one, two}})
	e.actor = a

	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-06", "14:00", "15:45", 8, clientIDs(two, three)))
	if status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	got := decode[map[string]any](t, body)
	want := map[string]any{"id": id, "session_date": "2026-10-06", "start_time": "14:00", "end_time": "15:45", "duration_minutes": float64(105), "hours": 1.75, "on_site": false, "status": "pending"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, present := got["notes"]; present {
		t.Errorf("an omitted optional field kept its old value: %v", got["notes"])
	}
	var names []string
	for _, c := range got["clients"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"Three", "Two"}) {
		t.Errorf("clients = %v, want Three and Two", names)
	}
	if got["category"].(map[string]any)["name"] != "Case Management" {
		t.Errorf("category = %v", got["category"])
	}
	if n := e.count(`SELECT count(*) FROM entry_clients WHERE entry_id = $1`, id); n != 2 {
		t.Errorf("%d client links stored, want 2", n)
	}
}

func TestUpdateEntry_SavingARejectedEntryMakesItPendingAgain(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	id := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", status: "rejected", reviewNote: "Add detail"})
	e.actor = a
	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-05", "09:00", "10:00", 1, `"notes":"More detail"`))
	if status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	if got := decode[map[string]any](t, body); got["status"] != "pending" || got["notes"] != "More detail" {
		t.Errorf("entry = %v", got)
	}
}

func TestUpdateEntry_MayKeepOrShrinkItsOwnWindowButNotOverlapAnother(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	mine := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "11:00"})
	other := e.entry(a, row{date: "2026-10-05", start: "11:00", end: "12:00"})
	rejected := e.entry(a, row{date: "2026-10-05", start: "09:30", end: "10:30", status: "rejected"})
	e.actor = a

	if status, body := e.call(http.MethodPut, "/entries/"+mine, e.body("2026-10-05", "09:00", "11:00", 1)); status != http.StatusOK {
		t.Fatalf("saving the same window = %d: %s", status, body)
	}
	if status, body := e.call(http.MethodPut, "/entries/"+mine, e.body("2026-10-05", "09:15", "10:45", 1)); status != http.StatusOK {
		t.Fatalf("shrinking = %d: %s", status, body)
	}
	status, body := e.call(http.MethodPut, "/entries/"+mine, e.body("2026-10-05", "10:00", "11:30", 1, clientIDs()))
	p := expectProblem(t, status, body, http.StatusConflict, "entry-overlap")
	if p.ConflictingEntry["id"] != other || p.ConflictingEntry["start_time"] != "11:00" || p.ConflictingEntry["end_time"] != "12:00" {
		t.Errorf("conflicting_entry = %v, want the 11:00 to 12:00 entry", p.ConflictingEntry)
	}
	var start string
	if err := e.conn.QueryRow(t.Context(), `SELECT to_char(start_time, 'HH24:MI') FROM entries WHERE id = $1`, mine).Scan(&start); err != nil || start != "09:15" {
		t.Errorf("a refused update changed the entry: start %q (%v)", start, err)
	}

	// A rejected entry ignores overlaps until it is saved again, because saving makes it pending.
	status, body = e.call(http.MethodPut, "/entries/"+rejected, e.body("2026-10-05", "09:30", "10:30", 1))
	expectProblem(t, status, body, http.StatusConflict, "entry-overlap")
	if got := e.count(`SELECT count(*) FROM entries WHERE id = $1 AND status = 'rejected'`, rejected); got != 1 {
		t.Error("a refused save changed the rejected entry's status")
	}
}

func TestUpdateEntry_RefusedUpdateLeavesTheClientsUnchanged(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	one, theirs := e.client(a, "One"), e.client(b, "Theirs")
	id := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", clients: []string{one}})
	e.actor = a
	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-05", "09:00", "10:00", 1, clientIDs(theirs)))
	expectProblem(t, status, body, http.StatusBadRequest, "validation")
	if n := e.count(`SELECT count(*) FROM entry_clients WHERE entry_id = $1 AND client_id = $2`, id, one); n != 1 {
		t.Error("a refused update removed the entry's client")
	}
}

func TestUpdateAndDeleteEntry_ApprovedEntriesAreLocked(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	one := e.client(a, "One")
	id := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", status: "approved", notes: "approved note", clients: []string{one}})
	e.actor = a

	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-05", "09:00", "10:00", 2, `"notes":"changed"`))
	p := expectProblem(t, status, body, http.StatusConflict, "entry-locked")
	if p.ConflictingEntry != nil {
		t.Error("a locked entry names a conflicting entry")
	}
	status, body = e.call(http.MethodDelete, "/entries/"+id, "")
	expectProblem(t, status, body, http.StatusConflict, "entry-locked")

	var notes, st string
	if err := e.conn.QueryRow(t.Context(), `SELECT notes, status FROM entries WHERE id = $1`, id).Scan(&notes, &st); err != nil || notes != "approved note" || st != "approved" {
		t.Errorf("approved entry changed: %q %q (%v)", notes, st, err)
	}
	if n := e.count(`SELECT count(*) FROM entry_clients WHERE entry_id = $1`, id); n != 1 {
		t.Errorf("approved entry has %d clients, want 1", n)
	}
}

func TestUpdateAndDeleteEntry_OtherUsersEntryIs404AndUntouched(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	theirs := e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00", notes: "original"})
	approved := e.entry(b, row{date: "2026-10-06", start: "09:00", end: "10:00", status: "approved"})

	// Even an admin supervisor of the same organization cannot reach another user's entries here.
	a.Admin, a.Supervisor = true, true
	e.actor = a
	for _, id := range []string{theirs, approved, "00000000-0000-4000-8000-00000000dead"} {
		status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-05", "09:00", "10:00", 1, `"notes":"hijacked"`))
		expectProblem(t, status, body, http.StatusNotFound, "not-found")
		status, body = e.call(http.MethodDelete, "/entries/"+id, "")
		expectProblem(t, status, body, http.StatusNotFound, "not-found")
		status, body = e.call(http.MethodGet, "/entries/"+id, "")
		expectProblem(t, status, body, http.StatusNotFound, "not-found")
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE id = $1 AND notes = 'original'`, theirs); n != 1 {
		t.Error("another user's entry was changed or deleted")
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE user_id = $1`, b.ID); n != 2 {
		t.Errorf("B has %d entries, want 2", n)
	}
}

func TestUpdateEntry_ValidationRunsBeforeTheLookup(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	theirs := e.entry(b, row{date: "2026-10-05", start: "09:00", end: "10:00"})
	e.actor = a
	// A malformed body is 400 whether or not the id exists, so it cannot be used to probe for ids.
	for _, id := range []string{theirs, "00000000-0000-4000-8000-00000000dead"} {
		status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-10-05", "10:00", "09:00", 1))
		expectProblem(t, status, body, http.StatusBadRequest, "validation")
	}
}

func TestDeleteEntry_RemovesPendingAndRejectedEntriesWithTheirClientLinks(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	one := e.client(a, "One")
	pending := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00", clients: []string{one}})
	rejected := e.entry(a, row{date: "2026-10-06", start: "09:00", end: "10:00", status: "rejected", clients: []string{one}})
	keep := e.entry(a, row{date: "2026-10-07", start: "09:00", end: "10:00", clients: []string{one}})
	e.actor = a

	for _, id := range []string{pending, rejected} {
		if status, body := e.call(http.MethodDelete, "/entries/"+id, ""); status != http.StatusNoContent || body != "" {
			t.Fatalf("DELETE = %d %q", status, body)
		}
		status, body := e.call(http.MethodGet, "/entries/"+id, "")
		expectProblem(t, status, body, http.StatusNotFound, "not-found")
		status, body = e.call(http.MethodDelete, "/entries/"+id, "")
		expectProblem(t, status, body, http.StatusNotFound, "not-found")
	}
	if got := e.list(nil); !slices.Equal(got.ids(), []string{keep}) {
		t.Errorf("remaining = %v, want [%s]", got.ids(), keep)
	}
	if n := e.count(`SELECT count(*) FROM entry_clients`); n != 1 {
		t.Errorf("%d client links remain, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM clients WHERE id = $1`, one); n != 1 {
		t.Error("deleting an entry deleted its client")
	}
	// The freed window can be booked again.
	e.created(e.body("2026-10-05", "09:00", "10:00", 1))
}

func TestEntryWrites_DeactivatedActorIsForbidden(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	id := e.entry(a, row{date: "2026-10-05", start: "09:00", end: "10:00"})
	a.Active = false
	e.actor = a
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/entries", e.body("2026-10-06", "09:00", "10:00", 1)},
		{http.MethodGet, "/entries/" + id, ""},
		{http.MethodPut, "/entries/" + id, e.body("2026-10-05", "09:00", "10:00", 1)},
		{http.MethodDelete, "/entries/" + id, ""},
	} {
		status, body := e.call(c.method, c.path, c.body)
		expectProblem(t, status, body, http.StatusForbidden, "forbidden")
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 1 {
		t.Errorf("entries = %d, want the one that existed", n)
	}
}
