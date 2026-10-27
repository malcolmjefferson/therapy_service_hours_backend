package entries_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	httpapi "github.com/malcolmjefferson/therapy_service_hours_backend/internal/http"
)

// S4-06 end-to-end tests: GET /export/entries through the real router, service and PostgreSQL store,
// against the real migrations (GO-17, GO-72). They reuse the helpers of store_test.go.

// export calls GET /export/entries as e.actor and returns the status and raw body.
func (e *env) export(params url.Values) (int, string) {
	e.t.Helper()
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + "/export/entries", RawQueryString: params.Encode()}
	event.RequestContext.HTTP.Method = http.MethodGet
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "s"}}}
	res, err := apigw.Handler(e.router, httpapi.ContextWithRequestID)(e.t.Context(), event)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.StatusCode, res.Body
}

func (e *env) exportPage(params url.Values) listBody {
	e.t.Helper()
	status, body := e.export(params)
	if status != http.StatusOK {
		e.t.Fatalf("GET /export/entries?%s = %d: %s", params.Encode(), status, body)
	}
	var out listBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		e.t.Fatalf("decode: %v: %s", err, body)
	}
	return out
}

func TestExportEntries_ReturnsOnlyTheCallersOwnEntries(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	clientA, clientB := e.client(a, "A.B."), e.client(b, "A.B.")
	mine := e.entry(a, row{date: "2026-10-01", start: "09:00", end: "10:00", clients: []string{clientA}})
	e.entry(b, row{date: "2026-10-01", start: "09:00", end: "10:00", notes: "someone else's note", clients: []string{clientB}})
	e.entry(b, row{date: "2026-10-02", start: "09:00", end: "10:00"})

	e.actor = a
	got := e.exportPage(nil)
	if !slices.Equal(got.ids(), []string{mine}) || got.Page.TotalItems != 1 {
		t.Fatalf("A exports %v of %d", got.ids(), got.Page.TotalItems)
	}
	if clients := got.Items[0]["clients"].([]any); len(clients) != 1 || clients[0].(map[string]any)["id"] != clientA {
		t.Errorf("A's entry carries clients %v, want only A's own", clients)
	}

	e.actor = b
	if got := e.exportPage(nil); got.Page.TotalItems != 2 || slices.Contains(got.ids(), mine) {
		t.Fatalf("B exports %v of %d", got.ids(), got.Page.TotalItems)
	}
}

func TestExportEntries_AdminAndSupervisorRolesStillExportOnlyTheirOwn(t *testing.T) {
	e := newEnv(t)
	boss, other := e.user("boss@example.test"), e.user("other@example.test")
	e.entry(other, row{date: "2026-10-01", start: "09:00", end: "10:00"})
	boss.Admin, boss.Supervisor = true, true
	e.actor = boss
	if got := e.exportPage(nil); got.Page.TotalItems != 0 || len(got.Items) != 0 {
		t.Fatalf("an admin supervisor exports %d entries of another user", got.Page.TotalItems)
	}
}

func TestExportEntries_ClientFiltersAreNotAvailableAndAnotherUsersIDIsIgnored(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	mine := e.entry(a, row{date: "2026-10-01", start: "09:00", end: "10:00"})
	e.entry(b, row{date: "2026-10-01", start: "11:00", end: "12:00"})
	e.actor = a
	// An export is the whole ledger: filters and a user id in the query string change nothing.
	got := e.exportPage(q("group", "supervision", "client", "nobody", "user_id", b.ID))
	if !slices.Equal(got.ids(), []string{mine}) {
		t.Fatalf("export = %v, want only %v", got.ids(), mine)
	}
}

func TestExportEntries_IncludesStartEndClientsAndStatus(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	zed, alpha := e.client(a, "Zed"), e.client(a, "alpha")
	e.entry(a, row{date: "2026-10-05", start: "13:15", end: "14:30", category: 11, onSite: true, notes: "Pseudonym note", status: "rejected", reviewNote: "Add detail", clients: []string{zed, alpha}})
	e.actor = a
	got := e.exportPage(nil)
	if len(got.Items) != 1 {
		t.Fatalf("items = %d", len(got.Items))
	}
	item := got.Items[0]
	want := map[string]any{
		"start_time": "13:15", "end_time": "14:30", "duration_minutes": float64(75), "hours": 1.25,
		"on_site": true, "notes": "Pseudonym note", "status": "rejected", "review_note": "Add detail", "needs_time": false,
	}
	for k, v := range want {
		if item[k] != v {
			t.Errorf("%s = %v, want %v", k, item[k], v)
		}
	}
	if cat := item["category"].(map[string]any); cat["name"] != "Group Supervision" || cat["group"] != "supervision" {
		t.Errorf("category = %v", cat)
	}
	var names []string
	for _, c := range item["clients"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"alpha", "Zed"}) {
		t.Errorf("clients = %v", names)
	}
}

func TestExportEntries_IncludesLegacyEntriesAndEveryStatus(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.entry(a, row{date: "2026-09-01", minutes: 90, category: 7, status: "approved"})
	e.entry(a, row{date: "2026-10-06", start: "08:00", end: "08:15", status: "approved"})
	e.entry(a, row{date: "2026-10-07", start: "08:00", end: "09:00", status: "pending"})
	e.entry(a, row{date: "2026-10-08", start: "08:00", end: "09:00", status: "rejected", reviewNote: "Add detail"})
	e.actor = a
	got := e.exportPage(nil)
	if len(got.Items) != 4 {
		t.Fatalf("items = %d, want every status", len(got.Items))
	}
	legacy := got.Items[0]
	for _, absent := range []string{"start_time", "end_time", "notes", "review_note"} {
		if _, present := legacy[absent]; present {
			t.Errorf("legacy entry has %q: %v", absent, legacy)
		}
	}
	if legacy["needs_time"] != true || legacy["hours"] != 1.5 {
		t.Errorf("legacy = %v", legacy)
	}
	statuses := map[any]int{}
	for _, it := range got.Items {
		statuses[it["status"]]++
	}
	if statuses["pending"] != 1 || statuses["approved"] != 2 || statuses["rejected"] != 1 {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestExportEntries_IsOldestFirstWithUntimedEntriesFirstWithinADay(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	late := e.entry(a, row{date: "2026-03-01", start: "15:00", end: "16:00"})
	newest := e.entry(a, row{date: "2026-07-01", start: "09:00", end: "10:00"})
	early := e.entry(a, row{date: "2026-03-01", start: "08:00", end: "09:00"})
	legacy := e.entry(a, row{date: "2026-03-01", minutes: 60})
	oldest := e.entry(a, row{date: "2026-01-05", start: "09:00", end: "10:00"})
	e.actor = a
	got := e.exportPage(nil)
	if want := []string{oldest, legacy, early, late, newest}; !slices.Equal(got.ids(), want) {
		t.Fatalf("order = %v, want %v", got.ids(), want)
	}
	if got.Page.Page != 1 || got.Page.PageSize != 50 {
		t.Errorf("page = %+v", got.Page)
	}
}

func TestExportEntries_PagesCoverTheLedgerOnceInAStableOrder(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	// Equal dates and equal start times (two untimed entries and several on the same slot cannot
	// overlap, so spread the timed ones) force the id tiebreaker to decide the order.
	const total = 11
	for i := range total {
		if i%3 == 0 {
			e.entry(a, row{date: "2026-04-01", minutes: 30})
			continue
		}
		hour := strconv.Itoa(8 + i)
		if len(hour) == 1 {
			hour = "0" + hour
		}
		e.entry(a, row{date: "2026-04-01", start: hour + ":00", end: hour + ":30"})
	}
	e.actor = a

	whole := e.exportPage(q("page_size", "200")).ids()
	if len(whole) != total {
		t.Fatalf("whole ledger = %d, want %d", len(whole), total)
	}
	var paged []string
	for page := 1; page <= 4; page++ {
		got := e.exportPage(q("page", strconv.Itoa(page), "page_size", "3"))
		if got.Page.TotalItems != total || got.Page.Page != page || got.Page.PageSize != 3 {
			t.Fatalf("page %d info = %+v", page, got.Page)
		}
		paged = append(paged, got.ids()...)
	}
	if !slices.Equal(paged, whole) {
		t.Fatalf("pages joined = %v\nwhole = %v", paged, whole)
	}
	if past := e.exportPage(q("page", "5", "page_size", "3")); len(past.Items) != 0 || past.Page.TotalItems != total {
		t.Errorf("a page past the end = %d items of %d, want none of %d", len(past.Items), past.Page.TotalItems, total)
	}
}

func TestExportEntries_PageSizeCapIsEnforced(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.entry(a, row{date: "2026-04-01", start: "09:00", end: "10:00"})
	e.actor = a
	for _, size := range []string{"201", "0", "-1", "1000000"} {
		if status, body := e.export(q("page_size", size)); status != http.StatusBadRequest {
			t.Errorf("page_size=%s = %d, want 400: %s", size, status, body)
		}
	}
	if status, body := e.export(q("page", "0")); status != http.StatusBadRequest {
		t.Errorf("page=0 = %d, want 400: %s", status, body)
	}
	if got := e.exportPage(q("page_size", "200")); got.Page.PageSize != 200 || len(got.Items) != 1 {
		t.Errorf("page_size=200 = %+v", got.Page)
	}
}

func TestExportEntries_EmptyLedger(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	status, body := e.export(nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	var got listBody
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Items == nil || len(got.Items) != 0 || got.Page.TotalItems != 0 {
		t.Errorf("body = %s (%v)", body, err)
	}
}
