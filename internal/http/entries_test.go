package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// S2-06 handler tests: the real router, generated binding, identity middleware and DTO mapping, with a
// fake service behind them. Names and notes are fictional pseudonyms (CLAUDE.md §0.7).

var entriesActor = domain.Actor{
	ID: "00000000-0000-4000-8000-000000000002", OrganizationID: "00000000-0000-4000-8000-000000000001",
	OrganizationName: "Default Organization", Email: "pseudonym@example.test", FirstName: "Pseudonym", LastName: "A", Active: true,
}

var entriesClaims = map[string]string{"sub": "trusted-sub", "email": entriesActor.Email, "email_verified": "true", "token_use": "id"}

// entriesCall issues GET /entries?query as the authenticated actor.
func entriesCall(t *testing.T, lister EntryLister, query string, claims map[string]string) (events.APIGatewayV2HTTPResponse, *logSink) {
	t.Helper()
	sink := &logSink{}
	deps := testDeps(sink)
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) { return entriesActor, nil })
	deps.Entries = lister
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatal(err)
	}
	event := meEvent(claims)
	event.RawPath = BasePath + "/entries"
	event.RawQueryString = query
	response, err := apigw.Handler(h, ContextWithRequestID)(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	return response, sink
}

func sampleEntry() domain.Entry {
	reviewed := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	return domain.Entry{
		ID:          "00000000-0000-4000-8000-0000000000e1",
		SessionDate: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		StartTime:   "09:00", EndTime: "10:15", DurationMinutes: 75,
		Category:   domain.CategoryRef{ID: "00000000-0000-4000-8000-0000000000c1", Name: "Individual Counseling", Group: domain.GroupDirect},
		Clients:    []domain.ClientRef{{ID: "00000000-0000-4000-8000-0000000000d1", Name: "A.B."}, {ID: "00000000-0000-4000-8000-0000000000d2", Name: "Group 1"}},
		OnSite:     true,
		Notes:      "Pseudonym note",
		Status:     domain.StatusRejected,
		ReviewNote: "Add the end time", ReviewedAt: &reviewed,
		CreatedAt: time.Date(2026, 10, 12, 17, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 12, 17, 30, 0, 0, time.UTC),
	}
}

func staticLister(p entries.Page, err error) EntryLister {
	return entryListerFunc(func(context.Context, domain.Actor, entries.ListRequest) (entries.Page, error) { return p, err })
}

func TestListEntries_ReturnsTheOwnerDTO(t *testing.T) {
	t.Parallel()
	legacy := domain.Entry{
		ID: "00000000-0000-4000-8000-0000000000e2", SessionDate: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), DurationMinutes: 90,
		Category: domain.CategoryRef{ID: "00000000-0000-4000-8000-0000000000c2", Name: "Staff Meetings", Group: domain.GroupIndirect},
		Status:   domain.StatusApproved, NeedsTime: true, CreatedAt: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC),
	}
	response, _ := entriesCall(t, staticLister(entries.Page{Items: []domain.Entry{sampleEntry(), legacy}, Page: 2, PageSize: 2, TotalItems: 7}, nil), "page=2&page_size=2", entriesClaims)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, response.Body)
	}
	if response.Headers["Cache-Control"] != "no-store" || response.Headers["Content-Type"] != "application/json" {
		t.Errorf("headers = %v", response.Headers)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Page  map[string]any   `json:"page"`
	}
	if err := json.Unmarshal([]byte(response.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Page["page"] != float64(2) || body.Page["page_size"] != float64(2) || body.Page["total_items"] != float64(7) {
		t.Errorf("page = %v", body.Page)
	}
	assertTimedEntry(t, body.Items[0])
	assertLegacyEntry(t, body.Items[1])
}

func assertTimedEntry(t *testing.T, e map[string]any) {
	t.Helper()
	want := map[string]any{
		"session_date": "2026-10-12", "start_time": "09:00", "end_time": "10:15", "duration_minutes": float64(75), "hours": 1.25,
		"status": "rejected", "review_note": "Add the end time", "on_site": true, "notes": "Pseudonym note", "needs_time": false,
	}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %v, want %v", k, e[k], v)
		}
	}
	if e["reviewed_at"] == nil {
		t.Error("reviewed_at is missing")
	}
	if cat := e["category"].(map[string]any); cat["name"] != "Individual Counseling" || cat["group"] != "direct" || len(cat) != 3 {
		t.Errorf("category = %v", cat)
	}
	if clients := e["clients"].([]any); len(clients) != 2 || clients[0].(map[string]any)["name"] != "A.B." {
		t.Errorf("clients = %v", e["clients"])
	}
	// The owner view is an explicit field list: nothing from storage (user ID, reviewer, category column) leaks.
	for _, leaked := range []string{"user_id", "reviewed_by", "category_id"} {
		if _, present := e[leaked]; present {
			t.Errorf("response has %q", leaked)
		}
	}
}

func assertLegacyEntry(t *testing.T, e map[string]any) {
	t.Helper()
	for _, absent := range []string{"start_time", "end_time", "notes", "review_note", "reviewed_at"} {
		if _, present := e[absent]; present {
			t.Errorf("legacy entry has %q, want it omitted: %v", absent, e)
		}
	}
	if e["needs_time"] != true || e["hours"] != 1.5 || len(e["clients"].([]any)) != 0 {
		t.Errorf("legacy entry = %v", e)
	}
}

func TestListEntries_EmptyPageIsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	response, _ := entriesCall(t, staticLister(entries.Page{Page: 1, PageSize: 50}, nil), "", entriesClaims)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Body, `"items":[]`) {
		t.Fatalf("status %d body %s", response.StatusCode, response.Body)
	}
}

func TestListEntries_ResponseMatchesTheSpec(t *testing.T) {
	t.Parallel()
	spec := loadSpec(t)
	specRouter, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := entriesCall(t, staticLister(entries.Page{Items: []domain.Entry{sampleEntry()}, Page: 1, PageSize: 50, TotalItems: 1}, nil), "group=direct&sort=hours&dir=asc", entriesClaims)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test"+BasePath+"/entries?group=direct&sort=hours&dir=asc", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, params, err := specRouter.FindRoute(req)
	if err != nil {
		t.Fatalf("GET /entries is not in the spec: %v", err)
	}
	header := http.Header{}
	for k, v := range response.Headers {
		header.Set(k, v)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 response.StatusCode,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader([]byte(response.Body))),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(t.Context(), input); err != nil {
		t.Errorf("response does not match the spec: %v\n%s", err, response.Body)
	}
}

func TestListEntries_PassesTheQueryStringToTheServiceUntouched(t *testing.T) {
	t.Parallel()
	var got entries.ListRequest
	var actor domain.Actor
	lister := entryListerFunc(func(_ context.Context, a domain.Actor, r entries.ListRequest) (entries.Page, error) {
		got, actor = r, a
		return entries.Page{}, nil
	})
	query := url.Values{"group": {"supervision"}, "client": {"  50%_\\ "}, "from": {"2026-01-01"}, "to": {"2026-01-31"}, "sort": {"on_site"}, "dir": {"asc"}, "page": {"3"}, "page_size": {"25"}}.Encode()
	response, _ := entriesCall(t, lister, query, entriesClaims)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, response.Body)
	}
	if actor.ID != entriesActor.ID {
		t.Errorf("service saw actor %q", actor.ID)
	}
	if *got.Group != "supervision" || *got.Client != "  50%_\\ " || *got.Sort != "on_site" || *got.Dir != "asc" || *got.Page != 3 || *got.PageSize != 25 {
		t.Errorf("request = %+v", got)
	}
	if got.From.Format(time.DateOnly) != "2026-01-01" || got.To.Format(time.DateOnly) != "2026-01-31" {
		t.Errorf("dates = %v %v", got.From, got.To)
	}
}

func TestListEntries_OmittedParametersStayNil(t *testing.T) {
	t.Parallel()
	var got entries.ListRequest
	lister := entryListerFunc(func(_ context.Context, _ domain.Actor, r entries.ListRequest) (entries.Page, error) {
		got = r
		return entries.Page{}, nil
	})
	if response, _ := entriesCall(t, lister, "", entriesClaims); response.StatusCode != http.StatusOK {
		t.Fatal(response.Body)
	}
	if got.Group != nil || got.Client != nil || got.From != nil || got.To != nil || got.Sort != nil || got.Dir != nil || got.Page != nil || got.PageSize != nil {
		t.Errorf("request = %+v, want every field nil", got)
	}
}

func TestListEntries_MalformedQueryIsA400BeforeTheService(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]string{
		"bad date":         "from=yesterday",
		"impossible date":  "to=2026-02-30",
		"page not numeric": "page=one",
		"page size float":  "page_size=1.5",
		"repeated sort":    "sort=hours&sort=category",
		"repeated client":  "client=a&client=b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lister := entryListerFunc(func(context.Context, domain.Actor, entries.ListRequest) (entries.Page, error) {
				t.Error("the service ran for a malformed query")
				return entries.Page{}, nil
			})
			response, _ := entriesCall(t, lister, query, entriesClaims)
			assertProblem(t, response, http.StatusBadRequest, ProblemValidation)
		})
	}
}

func TestListEntries_ValidationErrorsBecome400WithFieldsAndNoEchoedValues(t *testing.T) {
	t.Parallel()
	verr := domain.NewValidationError(domain.FieldError{Field: "sort", Message: "must be one of session_date, category, client, hours, on_site"})
	response, sink := entriesCall(t, staticLister(entries.Page{}, verr), "sort=secret-sort-value", entriesClaims)
	problem := assertProblem(t, response, http.StatusBadRequest, ProblemValidation)
	if len(problem.Errors) != 1 || problem.Errors[0].Field != "sort" {
		t.Errorf("errors = %+v", problem.Errors)
	}
	if strings.Contains(response.Body, "secret-sort-value") || strings.Contains(sink.buf.String(), "secret-sort-value") {
		t.Error("the submitted value was echoed or logged")
	}
}

func TestListEntries_FailuresAreRedactedProblems(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		err    error
		status int
	}{
		"forbidden":        {domain.ErrForbidden, http.StatusForbidden},
		"database failure": {errors.New("SQL pseudonym@example.test secret-password"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			response, sink := entriesCall(t, staticLister(entries.Page{}, tt.err), "client=A.B.", entriesClaims)
			if response.StatusCode != tt.status || response.Headers["Content-Type"] != "application/problem+json" {
				t.Fatalf("status %d type %q", response.StatusCode, response.Headers["Content-Type"])
			}
			for _, secret := range []string{"secret-password", "pseudonym@example.test", "SQL", "A.B."} {
				if strings.Contains(response.Body, secret) || strings.Contains(sink.buf.String(), secret) {
					t.Errorf("%q leaked", secret)
				}
			}
		})
	}
}

func TestListEntries_RequiresAnAuthorizedCaller(t *testing.T) {
	t.Parallel()
	lister := entryListerFunc(func(context.Context, domain.Actor, entries.ListRequest) (entries.Page, error) {
		t.Error("the service ran without an authorizer context")
		return entries.Page{}, nil
	})
	response, _ := entriesCall(t, lister, "", nil)
	assertProblem(t, response, http.StatusUnauthorized, ProblemUnauthorized)
}

func TestListEntries_StoredIDsThatAreNotUUIDsFailClosed(t *testing.T) {
	t.Parallel()
	bad := sampleEntry()
	bad.ID = "not-a-uuid"
	response, _ := entriesCall(t, staticLister(entries.Page{Items: []domain.Entry{bad}, Page: 1, PageSize: 50, TotalItems: 1}, nil), "", entriesClaims)
	assertProblem(t, response, http.StatusInternalServerError, ProblemInternal)
}

func assertProblem(t *testing.T, response events.APIGatewayV2HTTPResponse, status int, want ProblemType) Problem {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, status, response.Body)
	}
	p := decodeProblem(t, []byte(response.Body))
	if p.Type != want || response.Headers["Content-Type"] != "application/problem+json" {
		t.Fatalf("problem = %+v, content type %q", p, response.Headers["Content-Type"])
	}
	return p
}
