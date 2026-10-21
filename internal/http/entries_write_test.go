package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// S2-05 handler tests: the real router, middleware and generated strict server run against a fake
// entries service, and each response is validated against api/openapi.yaml. Names and notes are
// fictional pseudonyms (CLAUDE.md §0.7).

const (
	testEntryID    = "00000000-0000-4000-8000-0000000000e1"
	testCategoryID = "00000000-0000-4000-8000-0000000000c1"
)

// fakeEntries records its calls and answers with the configured results.
type fakeEntries struct {
	calls []string
	actor domain.Actor

	entry domain.Entry
	err   error
	gotID string
	gotIn entries.Input
}

func (f *fakeEntries) List(context.Context, domain.Actor, entries.ListRequest) (entries.Page, error) {
	return entries.Page{}, f.err
}

func (f *fakeEntries) Get(_ context.Context, a domain.Actor, id string) (domain.Entry, error) {
	f.calls, f.actor, f.gotID = append(f.calls, "get"), a, id
	return f.entry, f.err
}

func (f *fakeEntries) Create(_ context.Context, a domain.Actor, in entries.Input) (domain.Entry, error) {
	f.calls, f.actor, f.gotIn = append(f.calls, "create"), a, in
	return f.entry, f.err
}

func (f *fakeEntries) Update(_ context.Context, a domain.Actor, id string, in entries.Input) (domain.Entry, error) {
	f.calls, f.actor, f.gotID, f.gotIn = append(f.calls, "update"), a, id, in
	return f.entry, f.err
}

func (f *fakeEntries) Delete(_ context.Context, a domain.Actor, id string) error {
	f.calls, f.actor, f.gotID = append(f.calls, "delete"), a, id
	return f.err
}

// newEntriesHarness reuses the clients harness's sender and spec validator with a fake entries service.
func newEntriesHarness(t *testing.T) (*clientsHarness, *fakeEntries) {
	t.Helper()
	svc := &fakeEntries{entry: sampleEntry()}
	deps := testDeps(&logSink{})
	deps.MaxBodyBytes = 8192
	deps.Entries = svc
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &clientsHarness{t: t, handler: apigw.Handler(h, ContextWithRequestID), spec: loadSpec(t)}, svc
}

const entryBody = `{"session_date":"2026-10-12","start_time":"09:00","end_time":"10:15","category_id":"` + testCategoryID + `",` +
	`"client_ids":["` + testClient1 + `","` + testClient2 + `"],"on_site":true,"notes":"Pseudonym note"}`

func TestEntriesWrite_CreateMapsTheRequestAndReturnsTheOwnerDTO(t *testing.T) {
	t.Parallel()
	c, svc := newEntriesHarness(t)
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	want := entries.Input{
		SessionDate: "2026-10-12", StartTime: "09:00", EndTime: "10:15", CategoryID: testCategoryID,
		ClientIDs: []string{testClient1, testClient2}, OnSite: true, Notes: "Pseudonym note",
	}
	if !reflect.DeepEqual(svc.gotIn, want) {
		t.Errorf("service input = %+v, want %+v", svc.gotIn, want)
	}
	if svc.actor.ID != testUserID {
		t.Errorf("service did not receive the resolved actor: %+v", svc.actor)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != testEntryID || got["hours"] != 1.25 || got["duration_minutes"] != float64(75) || got["status"] != "rejected" {
		t.Errorf("body = %v", got)
	}
	if res.Headers["Cache-Control"] != "no-store" {
		t.Error("entry response is cacheable")
	}
	c.checkSpec(http.MethodPost, "/api/v1/entries", entryBody, res)
}

func TestEntriesWrite_OptionalFieldsDefaultToNone(t *testing.T) {
	t.Parallel()
	c, svc := newEntriesHarness(t)
	body := `{"session_date":"2026-10-12","start_time":"09:00","end_time":"10:00","category_id":"` + testCategoryID + `"}`
	if res := c.send(http.MethodPost, "/api/v1/entries", body, keyed()); res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if svc.gotIn.OnSite || svc.gotIn.Notes != "" || len(svc.gotIn.ClientIDs) != 0 {
		t.Errorf("defaults = %+v", svc.gotIn)
	}
}

func TestEntriesWrite_RequestsTheContractRejectsNeverReachTheService(t *testing.T) {
	t.Parallel()
	valid := `"session_date":"2026-10-12","start_time":"09:00","end_time":"10:00","category_id":"` + testCategoryID + `"`
	tests := []struct {
		name, method, target, body string
		headers                    map[string]string
	}{
		{"client-sent hours on create", http.MethodPost, "/api/v1/entries", `{` + valid + `,"hours":2}`, keyed()},
		{"client-sent duration on create", http.MethodPost, "/api/v1/entries", `{` + valid + `,"duration_minutes":120}`, keyed()},
		{"client-sent hours on update", http.MethodPut, "/api/v1/entries/" + testEntryID, `{` + valid + `,"hours":2}`, nil},
		{"unknown field", http.MethodPost, "/api/v1/entries", `{` + valid + `,"status":"approved"}`, keyed()},
		{"missing category", http.MethodPost, "/api/v1/entries", `{"session_date":"2026-10-12","start_time":"09:00","end_time":"10:00"}`, keyed()},
		{"missing idempotency key", http.MethodPost, "/api/v1/entries", `{` + valid + `}`, nil},
		{"entry id is not a UUID", http.MethodGet, "/api/v1/entries/not-a-uuid", "", nil},
		{"update with a bad entry id", http.MethodPut, "/api/v1/entries/not-a-uuid", `{` + valid + `}`, nil},
		{"delete with a bad entry id", http.MethodDelete, "/api/v1/entries/not-a-uuid", "", nil},
		{"too many clients", http.MethodPost, "/api/v1/entries", `{` + valid + `,"client_ids":[` + strings.TrimSuffix(strings.Repeat(`"`+testClient1+`",`, 51), ",") + `]}`, keyed()},
		{"notes over the limit", http.MethodPost, "/api/v1/entries", `{` + valid + `,"notes":"` + strings.Repeat("x", 2001) + `"}`, keyed()},
		{"start time not a time", http.MethodPost, "/api/v1/entries", `{"session_date":"2026-10-12","start_time":"9am","end_time":"10:00","category_id":"` + testCategoryID + `"}`, keyed()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, svc := newEntriesHarness(t)
			res := c.send(tt.method, tt.target, tt.body, tt.headers)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", res.StatusCode, res.Body)
			}
			if p := problemOf(t, res); p["type"] != string(ProblemValidation) {
				t.Errorf("problem = %v", p)
			}
			if len(svc.calls) != 0 {
				t.Errorf("service was called: %v", svc.calls)
			}
			if strings.Contains(res.Body, "Pseudonym") {
				t.Errorf("response echoes request content: %s", res.Body)
			}
			c.checkSpec(tt.method, tt.target, tt.body, res)
		})
	}
}

func TestEntriesWrite_ServiceValidationErrorIsA400ThatNamesEachField(t *testing.T) {
	t.Parallel()
	c, svc := newEntriesHarness(t)
	svc.err = domain.NewValidationError(
		domain.FieldError{Field: "end_time", Message: "must be after start_time; a session cannot cross midnight"},
		domain.FieldError{Field: "notes", Message: "must be at most 2000 characters"},
	)
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	p := problemOf(t, res)
	if fields, _ := p["errors"].([]any); len(fields) != 2 {
		t.Errorf("errors = %v", p["errors"])
	}
	c.checkSpec(http.MethodPost, "/api/v1/entries", entryBody, res)
}

func TestEntriesWrite_OverlapIs409NamingTheClashingSession(t *testing.T) {
	t.Parallel()
	clash := domain.ConflictingEntry{ID: "00000000-0000-4000-8000-0000000000e9", SessionDate: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), StartTime: "09:00", EndTime: "10:00"}
	for _, tt := range []struct{ name, method, target string }{
		{"create", http.MethodPost, "/api/v1/entries"},
		{"update", http.MethodPut, "/api/v1/entries/" + testEntryID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, svc := newEntriesHarness(t)
			svc.err = &domain.EntryOverlapError{Conflicting: clash}
			res := c.send(tt.method, tt.target, entryBody, keyed())
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
			p := problemOf(t, res)
			if p["type"] != string(ProblemEntryOverlap) {
				t.Errorf("type = %v", p["type"])
			}
			want := map[string]any{"id": clash.ID, "session_date": "2026-10-12", "start_time": "09:00", "end_time": "10:00"}
			if !reflect.DeepEqual(p["conflicting_entry"], want) {
				t.Errorf("conflicting_entry = %v, want %v", p["conflicting_entry"], want)
			}
			c.checkSpec(tt.method, tt.target, entryBody, res)
		})
	}
}

func TestEntriesWrite_LockedEntryIs409(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, method, body string }{
		{"update", http.MethodPut, entryBody},
		{"delete", http.MethodDelete, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, svc := newEntriesHarness(t)
			svc.err = &domain.EntryLockedError{}
			target := "/api/v1/entries/" + testEntryID
			res := c.send(tt.method, target, tt.body, nil)
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
			p := problemOf(t, res)
			if p["type"] != string(ProblemEntryLocked) {
				t.Errorf("type = %v", p["type"])
			}
			if _, present := p["conflicting_entry"]; present {
				t.Error("a locked entry names a conflicting entry")
			}
			c.checkSpec(tt.method, target, tt.body, res)
		})
	}
}

func TestEntriesWrite_MissingOrForeignEntryIs404AndForbiddenIs403(t *testing.T) {
	t.Parallel()
	target := "/api/v1/entries/" + testEntryID
	ops := []struct{ name, method, body string }{
		{"get", http.MethodGet, ""}, {"update", http.MethodPut, entryBody}, {"delete", http.MethodDelete, ""},
	}
	for _, op := range ops {
		for _, tt := range []struct {
			name   string
			err    error
			status int
		}{{"not found", domain.ErrNotFound, http.StatusNotFound}, {"forbidden", domain.ErrForbidden, http.StatusForbidden}} {
			t.Run(op.name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				c, svc := newEntriesHarness(t)
				svc.err = tt.err
				res := c.send(op.method, target, op.body, nil)
				if res.StatusCode != tt.status {
					t.Fatalf("status = %d, want %d (body %s)", res.StatusCode, tt.status, res.Body)
				}
				problemOf(t, res)
				if svc.gotID != testEntryID {
					t.Errorf("service got id %q", svc.gotID)
				}
				c.checkSpec(op.method, target, op.body, res)
			})
		}
	}
}

func TestEntriesWrite_GetUpdateAndDeleteSucceed(t *testing.T) {
	t.Parallel()
	target := "/api/v1/entries/" + testEntryID
	c, svc := newEntriesHarness(t)

	res := c.send(http.MethodGet, target, "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, testEntryID) {
		t.Fatalf("get = %d %s", res.StatusCode, res.Body)
	}
	c.checkSpec(http.MethodGet, target, "", res)

	res = c.send(http.MethodPut, target, entryBody, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", res.StatusCode, res.Body)
	}
	if svc.gotID != testEntryID || svc.gotIn.StartTime != "09:00" {
		t.Errorf("update got %q %+v", svc.gotID, svc.gotIn)
	}
	c.checkSpec(http.MethodPut, target, entryBody, res)

	res = c.send(http.MethodDelete, target, "", nil)
	if res.StatusCode != http.StatusNoContent || res.Body != "" {
		t.Fatalf("delete = %d %q", res.StatusCode, res.Body)
	}
	c.checkSpec(http.MethodDelete, target, "", res)
	if want := []string{"get", "update", "delete"}; !reflect.DeepEqual(svc.calls, want) {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}
}

func TestEntriesWrite_UnauthenticatedRequestsNeverReachTheService(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ method, target, body string }{
		{http.MethodPost, "/api/v1/entries", entryBody},
		{http.MethodGet, "/api/v1/entries/" + testEntryID, ""},
		{http.MethodPut, "/api/v1/entries/" + testEntryID, entryBody},
		{http.MethodDelete, "/api/v1/entries/" + testEntryID, ""},
	} {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			c, svc := newEntriesHarness(t)
			res := c.sendAs(tt.method, tt.target, tt.body, keyed(), nil)
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
			if len(svc.calls) != 0 {
				t.Errorf("service was called: %v", svc.calls)
			}
		})
	}
}
