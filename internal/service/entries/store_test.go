package entries_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	httpapi "github.com/malcolmjefferson/therapy_service_hours_backend/internal/http"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/categories"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/clients"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

// S2-06 end-to-end tests: GET /entries through the real router, service and PostgreSQL store, against
// the real migrations (GO-17, GO-72). Users, clients and notes are fictional pseudonyms (CLAUDE.md §0.7).

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

// env is one migrated database with the production wiring on top and a way to call the endpoint.
type env struct {
	t      *testing.T
	conn   *pgx.Conn
	pool   *pgxpool.Pool
	router http.Handler
	actor  domain.Actor // who the next request is made as
	actors sync.Map     // subject -> domain.Actor, for requests made as another user at the same time
	keys   atomic.Int64 // counter behind the fresh Idempotency-Key of each POST
}

func newEnv(t *testing.T) *env {
	t.Helper()
	database := server.NewDatabase(t)
	pool, err := db.Open(t.Context(), db.Settings{
		Host: server.Host, Port: server.Port, Database: database, User: server.User,
		Mode: db.AuthPassword, Password: server.Password,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	res := migrate.Run(t.Context(), sqlDB, migrations.FS, migrate.Options{})
	_ = sqlDB.Close()
	if res.Status != migrate.StatusOK {
		t.Fatalf("migrate: %+v", res)
	}
	e := &env{t: t, conn: server.Connect(t, database), pool: pool}

	service, err := entries.New(db.NewEntryStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	clientService, err := clients.New(db.NewClientStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	categoryService, err := categories.New(db.NewCategoryStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	e.router, err = httpapi.NewRouter(httpapi.RouterDeps{
		Logger:        quietLogger(),
		Version:       "test",
		MaxBodyBytes:  1 << 20,
		NewRequestID:  func() string { return "request-id" },
		Now:           time.Now,
		ActorResolver: actorFunc(e.resolve),
		Entries:       service,
		Clients:       clientService,
		Categories:    categoryService,
		Idempotency:   db.NewIdempotencyStore(pool),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// resolve returns the actor registered for the request's subject (see callAs), else e.actor.
func (e *env) resolve(_ context.Context, claims map[string]string) (domain.Actor, error) {
	if a, ok := e.actors.Load(claims["sub"]); ok {
		return a.(domain.Actor), nil
	}
	return e.actor, nil
}

// user inserts an active user in the seeded organization and returns the actor for it.
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

func (e *env) client(user domain.Actor, name string) string {
	e.t.Helper()
	var id string
	if err := e.conn.QueryRow(e.t.Context(), `INSERT INTO clients (user_id, name) VALUES ($1, $2) RETURNING id::text`, user.ID, name).Scan(&id); err != nil {
		e.t.Fatalf("insert client %q: %v", name, err)
	}
	return id
}

// row describes one entry to insert. Category is the display position in CLAUDE.md §8; an empty
// Start makes a legacy entry without times, whose length is Minutes.
type row struct {
	date, start, end string
	minutes          int
	category         int
	onSite           bool
	notes            string
	status           string
	reviewNote       string
	clients          []string
}

func (e *env) entry(user domain.Actor, r row) string {
	e.t.Helper()
	if r.category == 0 {
		r.category = 1
	}
	if r.status == "" {
		r.status = "pending"
	}
	if r.start != "" {
		start, _ := time.Parse("15:04", r.start)
		end, _ := time.Parse("15:04", r.end)
		r.minutes = int(end.Sub(start) / time.Minute)
	}
	var id string
	err := e.conn.QueryRow(e.t.Context(), `
INSERT INTO entries (user_id, session_date, start_time, end_time, duration_minutes, category_id, on_site, notes, status, review_note, needs_time)
VALUES ($1, $2::date, NULLIF($3, '')::time, NULLIF($4, '')::time, $5, (SELECT id FROM categories WHERE sort_order = $6), $7, NULLIF($8, ''), $9, NULLIF($10, ''), $3 = '')
RETURNING id::text`,
		user.ID, r.date, r.start, r.end, r.minutes, r.category, r.onSite, r.notes, r.status, r.reviewNote).Scan(&id)
	if err != nil {
		e.t.Fatalf("insert entry %+v: %v", r, err)
	}
	for _, c := range r.clients {
		if _, err := e.conn.Exec(e.t.Context(), `INSERT INTO entry_clients (entry_id, client_id, user_id) VALUES ($1, $2, $3)`, id, c, user.ID); err != nil {
			e.t.Fatalf("attach client: %v", err)
		}
	}
	return id
}

type listBody struct {
	Items []map[string]any `json:"items"`
	Page  struct {
		Page       int   `json:"page"`
		PageSize   int   `json:"page_size"`
		TotalItems int64 `json:"total_items"`
	} `json:"page"`
}

func (b listBody) ids() []string {
	out := make([]string, len(b.Items))
	for i, it := range b.Items {
		out[i] = it["id"].(string)
	}
	return out
}

// get calls GET /entries as e.actor and returns the status and raw body.
func (e *env) get(params url.Values) (int, string) {
	e.t.Helper()
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + "/entries", RawQueryString: params.Encode()}
	event.RequestContext.HTTP.Method = http.MethodGet
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "s"}}}
	res, err := apigw.Handler(e.router, httpapi.ContextWithRequestID)(e.t.Context(), event)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.StatusCode, res.Body
}

func (e *env) list(params url.Values) listBody {
	e.t.Helper()
	status, body := e.get(params)
	if status != http.StatusOK {
		e.t.Fatalf("GET /entries?%s = %d: %s", params.Encode(), status, body)
	}
	var out listBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		e.t.Fatalf("decode: %v: %s", err, body)
	}
	return out
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func TestListEntries_ReturnsOnlyTheCallersOwnEntries(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	clientA, clientB := e.client(a, "A.B."), e.client(b, "A.B.")
	mine := e.entry(a, row{date: "2026-10-01", start: "09:00", end: "10:00", clients: []string{clientA}})
	e.entry(b, row{date: "2026-10-01", start: "09:00", end: "10:00", notes: "someone else's note", clients: []string{clientB}})
	e.entry(b, row{date: "2026-10-02", start: "09:00", end: "10:00"})

	e.actor = a
	got := e.list(nil)
	if !slices.Equal(got.ids(), []string{mine}) || got.Page.TotalItems != 1 {
		t.Fatalf("A sees %v of %d", got.ids(), got.Page.TotalItems)
	}
	if clients := got.Items[0]["clients"].([]any); len(clients) != 1 || clients[0].(map[string]any)["id"] != clientA {
		t.Errorf("A's entry carries clients %v, want only A's own", clients)
	}

	e.actor = b
	if got := e.list(nil); got.Page.TotalItems != 2 || slices.Contains(got.ids(), mine) {
		t.Fatalf("B sees %v of %d", got.ids(), got.Page.TotalItems)
	}
}

func TestListEntries_AdminAndSupervisorRolesStillSeeOnlyTheirOwn(t *testing.T) {
	e := newEnv(t)
	boss, other := e.user("boss@example.test"), e.user("other@example.test")
	e.entry(other, row{date: "2026-10-01", start: "09:00", end: "10:00"})
	boss.Admin, boss.Supervisor = true, true
	e.actor = boss
	if got := e.list(nil); got.Page.TotalItems != 0 || len(got.Items) != 0 {
		t.Fatalf("an admin supervisor sees %d entries of another user", got.Page.TotalItems)
	}
}

func TestListEntries_MapsEveryFieldOfTheOwnerView(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	zed, alpha, mid := e.client(a, "Zed"), e.client(a, "alpha"), e.client(a, "Mid")
	e.entry(a, row{date: "2026-10-05", start: "13:15", end: "14:30", category: 11, onSite: true, notes: "Pseudonym note", status: "rejected", reviewNote: "Add detail", clients: []string{zed, alpha, mid}})
	e.entry(a, row{date: "2026-09-01", minutes: 90, category: 7, status: "approved"})

	e.actor = a
	got := e.list(nil)
	if len(got.Items) != 2 {
		t.Fatalf("items = %d", len(got.Items))
	}
	timed, legacy := got.Items[0], got.Items[1]
	want := map[string]any{
		"session_date": "2026-10-05", "start_time": "13:15", "end_time": "14:30", "duration_minutes": float64(75), "hours": 1.25,
		"on_site": true, "notes": "Pseudonym note", "status": "rejected", "review_note": "Add detail", "needs_time": false,
	}
	for k, v := range want {
		if timed[k] != v {
			t.Errorf("%s = %v, want %v", k, timed[k], v)
		}
	}
	if cat := timed["category"].(map[string]any); cat["name"] != "Group Supervision" || cat["group"] != "supervision" {
		t.Errorf("category = %v", cat)
	}
	var names []string
	for _, c := range timed["clients"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if want := []string{"alpha", "Mid", "Zed"}; !slices.Equal(names, want) {
		t.Errorf("clients = %v, want %v (case-insensitive order)", names, want)
	}
	for _, absent := range []string{"start_time", "end_time", "notes", "review_note", "reviewed_at"} {
		if _, present := legacy[absent]; present {
			t.Errorf("legacy entry has %q: %v", absent, legacy)
		}
	}
	if legacy["needs_time"] != true || legacy["hours"] != 1.5 || len(legacy["clients"].([]any)) != 0 {
		t.Errorf("legacy = %v", legacy)
	}
}

func TestListEntries_DefaultOrderIsNewestFirst(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	oldest := e.entry(a, row{date: "2026-01-05", start: "09:00", end: "10:00"})
	sameDayEarly := e.entry(a, row{date: "2026-03-01", start: "08:00", end: "09:00"})
	sameDayLate := e.entry(a, row{date: "2026-03-01", start: "15:00", end: "16:00"})
	legacy := e.entry(a, row{date: "2026-03-01", minutes: 60})
	e.actor = a
	got := e.list(nil)
	// Newest date first; within a day later starts first and entries without times last.
	if want := []string{sameDayLate, sameDayEarly, legacy, oldest}; !slices.Equal(got.ids(), want) {
		t.Fatalf("order = %v, want %v", got.ids(), want)
	}
	if got.Page.Page != 1 || got.Page.PageSize != 50 {
		t.Errorf("page = %+v", got.Page)
	}
}

func TestListEntries_Filters(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	ab, group := e.client(a, "A.B."), e.client(a, "Group 1")
	direct := e.entry(a, row{date: "2026-02-01", start: "09:00", end: "10:00", category: 1, clients: []string{ab}})
	indirect := e.entry(a, row{date: "2026-02-15", start: "09:00", end: "10:00", category: 7, clients: []string{group}})
	supervision := e.entry(a, row{date: "2026-03-01", start: "09:00", end: "10:00", category: 11})
	e.actor = a

	tests := []struct {
		name   string
		params url.Values
		want   []string
	}{
		{"group direct", q("group", "direct"), []string{direct}},
		{"group indirect", q("group", "indirect"), []string{indirect}},
		{"group supervision", q("group", "supervision"), []string{supervision}},
		{"from is inclusive", q("from", "2026-02-15"), []string{supervision, indirect}},
		{"to is inclusive", q("to", "2026-02-15"), []string{indirect, direct}},
		{"single day", q("from", "2026-02-15", "to", "2026-02-15"), []string{indirect}},
		{"client substring ignoring case", q("client", "a.b"), []string{direct}},
		{"client matches any of several", q("client", "GROUP"), []string{indirect}},
		{"client with no match", q("client", "nobody"), nil},
		{"filters combine with AND", q("group", "direct", "client", "group"), nil},
		{"all filters", q("group", "indirect", "client", "1", "from", "2026-02-01", "to", "2026-02-28"), []string{indirect}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.list(tt.params)
			if !slices.Equal(got.ids(), tt.want) || got.Page.TotalItems != int64(len(tt.want)) {
				t.Errorf("ids = %v total %d, want %v", got.ids(), got.Page.TotalItems, tt.want)
			}
			if got.Items == nil {
				t.Error("items is null, want an array")
			}
		})
	}
}

func TestListEntries_LikeWildcardsInTheClientFilterMatchLiterally(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	names := map[string]string{
		"50% done": e.client(a, "50% done"), "a_b": e.client(a, "a_b"), "axb": e.client(a, "axb"),
		`back\slash`: e.client(a, `back\slash`), "plain": e.client(a, "plain"), "100%": e.client(a, "100%"),
	}
	byName := map[string]string{}
	minute := 0
	for name, clientID := range names {
		start := fmt.Sprintf("%02d:00", 8+minute)
		minute++
		byName[name] = e.entry(a, row{date: "2026-10-01", start: start, end: fmt.Sprintf("%02d:15", 8+minute-1), clients: []string{clientID}})
	}
	e.actor = a
	tests := []struct {
		filter string
		want   []string
	}{
		{"%", []string{"50% done", "100%"}},
		{"_", []string{"a_b"}},
		{"a_b", []string{"a_b"}}, // not "axb": _ is not a one-character wildcard
		{`\`, []string{`back\slash`}},
		{"%%", nil},
		{"50%", []string{"50% done"}},
		{"%one", nil}, // % is not "anything": there is no client containing the text "%one"
		{`k\s`, []string{`back\slash`}},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			got := e.list(q("client", tt.filter))
			var want []string
			for _, n := range tt.want {
				want = append(want, byName[n])
			}
			if !sameSet(got.ids(), want) {
				t.Errorf("client=%q returned %d entries, want %v", tt.filter, len(got.ids()), tt.want)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func TestListEntries_InvalidInputIs400AndNeverReachesSQL(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.entry(a, row{date: "2026-10-01", start: "09:00", end: "10:00"})
	e.actor = a
	payloads := []string{"' OR 1=1 --", "'; DROP TABLE entries; --", `"; DROP TABLE users; --`, "session_date; DROP TABLE entries", "user_id", "id", "notes", "hours desc", "1"}
	for _, bad := range []string{"sort", "dir", "group"} {
		for _, p := range payloads {
			status, body := e.get(q(bad, p))
			if status != http.StatusBadRequest {
				t.Errorf("%s=%q: status %d, want 400: %s", bad, p, status, body)
			}
			if strings.Contains(body, "DROP") || strings.Contains(body, "OR 1=1") {
				t.Errorf("%s=%q echoed in the response: %s", bad, p, body)
			}
		}
	}
	for _, p := range payloads {
		// A client filter is data, never SQL: injection text is a plain substring that matches nothing.
		if status, body := e.get(q("client", p)); status != http.StatusOK {
			t.Errorf("client=%q: status %d: %s", p, status, body)
		}
	}
	for _, p := range []string{"a\x00b", "a\xffb", strings.Repeat("x", 65), "", "   "} {
		if status, _ := e.get(q("client", p)); status != http.StatusBadRequest {
			t.Errorf("client=%q: status %d, want 400", p, status)
		}
	}
	var n int
	if err := e.conn.QueryRow(t.Context(), `SELECT count(*) FROM entries`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("entries table after the payloads: count %d err %v", n, err)
	}
	if status, _ := e.get(q("sort", "hours", "dir", "asc")); status != http.StatusOK {
		t.Errorf("valid sort now fails: %d", status)
	}
}

func TestListEntries_PaginationIsStableUnderEqualDatesForEverySort(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	// Nine sessions on one day with the same category, duration and on-site flag, so every sort key ties.
	want := map[string]bool{}
	for h := range 9 {
		id := e.entry(a, row{date: "2026-10-01", start: fmt.Sprintf("%02d:00", 8+h), end: fmt.Sprintf("%02d:30", 8+h)})
		want[id] = true
	}
	e.actor = a
	for _, sort := range []string{"session_date", "category", "client", "hours", "on_site"} {
		for _, dir := range []string{"asc", "desc"} {
			t.Run(sort+" "+dir, func(t *testing.T) {
				var all []string
				for page := 1; page <= 3; page++ {
					got := e.list(q("sort", sort, "dir", dir, "page", fmt.Sprint(page), "page_size", "4"))
					if got.Page.TotalItems != 9 || got.Page.Page != page || got.Page.PageSize != 4 {
						t.Fatalf("page meta = %+v", got.Page)
					}
					all = append(all, got.ids()...)
				}
				seen := map[string]bool{}
				for _, id := range all {
					if seen[id] || !want[id] {
						t.Fatalf("page contents repeat or invent %s: %v", id, all)
					}
					seen[id] = true
				}
				if len(seen) != 9 {
					t.Fatalf("pages cover %d of 9 entries", len(seen))
				}
				// Asking again returns the same order.
				again := e.list(q("sort", sort, "dir", dir, "page", "2", "page_size", "4"))
				if !slices.Equal(again.ids(), all[4:8]) {
					t.Errorf("page 2 changed between calls")
				}
			})
		}
	}
	if got := e.list(q("page", "99", "page_size", "4")); len(got.Items) != 0 || got.Page.TotalItems != 9 {
		t.Errorf("a page past the end = %d items of %d", len(got.Items), got.Page.TotalItems)
	}
}

func TestListEntries_CategorySortIsGroupThenName(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	// Display positions: 11 Group Supervision, 1 Individual Counseling, 7 Staff Meetings, 2 Group Facilitation, 6 Writing Progress Notes.
	ids := map[int]string{}
	for i, cat := range []int{11, 1, 7, 2, 6} {
		ids[cat] = e.entry(a, row{date: "2026-10-01", start: fmt.Sprintf("%02d:00", 8+i), end: fmt.Sprintf("%02d:30", 8+i), category: cat})
	}
	e.actor = a
	// direct: Group Facilitation, Individual Counseling; indirect: Staff Meetings, Writing Progress Notes; supervision.
	want := []string{ids[2], ids[1], ids[7], ids[6], ids[11]}
	if got := e.list(q("sort", "category", "dir", "asc")).ids(); !slices.Equal(got, want) {
		t.Errorf("asc = %v, want %v", got, want)
	}
	slices.Reverse(want)
	if got := e.list(q("sort", "category", "dir", "desc")).ids(); !slices.Equal(got, want) {
		t.Errorf("desc = %v, want %v", got, want)
	}
}

func TestListEntries_EachSortOrdersByItsColumn(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	zed, amy := e.client(a, "Zed"), e.client(a, "amy")
	long := e.entry(a, row{date: "2026-10-01", start: "08:00", end: "10:00", onSite: true, clients: []string{zed}})
	short := e.entry(a, row{date: "2026-10-02", start: "08:00", end: "08:30", clients: []string{amy}})
	none := e.entry(a, row{date: "2026-10-03", start: "08:00", end: "09:00"})
	e.actor = a
	tests := []struct {
		sort, dir string
		want      []string
	}{
		{"hours", "asc", []string{short, none, long}},
		{"hours", "desc", []string{long, none, short}},
		{"session_date", "asc", []string{long, short, none}},
		{"on_site", "desc", []string{long, none, short}},
		{"client", "asc", []string{short, long, none}}, // amy, Zed, then entries without a client last
		{"client", "desc", []string{long, short, none}},
	}
	for _, tt := range tests {
		t.Run(tt.sort+" "+tt.dir, func(t *testing.T) {
			if got := e.list(q("sort", tt.sort, "dir", tt.dir)).ids(); !slices.Equal(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListEntries_CountAgreesWithTheRowsAcrossFilters(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	c := e.client(a, "A.B.")
	for d := range 12 {
		e.entry(a, row{date: fmt.Sprintf("2026-10-%02d", d+1), start: "09:00", end: "10:00", category: 1 + d%12, clients: []string{c}})
	}
	e.actor = a
	got := e.list(q("from", "2026-10-03", "to", "2026-10-10", "page_size", "3", "page", "3"))
	if got.Page.TotalItems != 8 || len(got.Items) != 2 {
		t.Errorf("total %d with %d items on the last page, want 8 and 2", got.Page.TotalItems, len(got.Items))
	}
}

// TestListCategories_ServesTheSeededGroupsThroughTheRealRouter runs GET /categories through the real
// router, service and store against PostgreSQL (S2-08).
func TestListCategories_ServesTheSeededGroupsThroughTheRealRouter(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + "/categories"}
	event.RequestContext.HTTP.Method = http.MethodGet
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "s"}}}
	res, err := apigw.Handler(e.router, httpapi.ContextWithRequestID)(e.t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Headers["Cache-Control"] != "private, max-age=3600" {
		t.Fatalf("status = %d, Cache-Control = %q, body %s", res.StatusCode, res.Headers["Cache-Control"], res.Body)
	}
	var body struct {
		Groups []struct {
			Group      string `json:"group"`
			Label      string `json:"label"`
			Categories []struct {
				Name string `json:"name"`
			} `json:"categories"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(res.Body), &body); err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"direct": 5, "indirect": 5, "supervision": 2}
	if len(body.Groups) != 3 || body.Groups[0].Group != "direct" || body.Groups[1].Group != "indirect" || body.Groups[2].Group != "supervision" {
		t.Fatalf("groups = %+v", body.Groups)
	}
	for _, g := range body.Groups {
		if len(g.Categories) != wantCounts[g.Group] {
			t.Errorf("%s has %d categories, want %d", g.Group, len(g.Categories), wantCounts[g.Group])
		}
	}
	if body.Groups[0].Label != "Direct Service" || body.Groups[0].Categories[0].Name != "Individual Counseling" || body.Groups[2].Categories[1].Name != "Individual Supervision" {
		t.Errorf("order or labels wrong: %+v", body.Groups)
	}
}
