package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// S4-06 handler tests: GET /export/entries through the real router, generated binding, identity
// middleware and DTO mapping, with a fake service behind them. Names and notes are fictional
// pseudonyms (CLAUDE.md §0.7).

// exporter replaces only Export on a service that refuses everything else.
type exporter struct {
	EntryService
	export func(context.Context, domain.Actor, entries.ExportRequest) (entries.Page, error)
}

func (e exporter) Export(ctx context.Context, a domain.Actor, r entries.ExportRequest) (entries.Page, error) {
	return e.export(ctx, a, r)
}

func staticExporter(p entries.Page, err error) EntryService {
	return exporter{export: func(context.Context, domain.Actor, entries.ExportRequest) (entries.Page, error) { return p, err }}
}

func exportCall(t *testing.T, svc EntryService, query string, claims map[string]string) (resp responseAndLog) {
	t.Helper()
	response, sink := entriesCallAt(t, svc, "/export/entries", query, claims)
	return responseAndLog{response.StatusCode, response.Headers, response.Body, sink.buf.String()}
}

type responseAndLog struct {
	status  int
	headers map[string]string
	body    string
	logs    string
}

func legacyEntry() domain.Entry {
	return domain.Entry{
		ID: "00000000-0000-4000-8000-0000000000e2", SessionDate: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), DurationMinutes: 90,
		Category: domain.CategoryRef{ID: "00000000-0000-4000-8000-0000000000c2", Name: "Staff Meetings", Group: domain.GroupIndirect},
		Status:   domain.StatusApproved, NeedsTime: true, CreatedAt: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC),
	}
}

func TestExportEntries_ReturnsEveryFieldAnExportNeeds(t *testing.T) {
	t.Parallel()
	page := entries.Page{Items: []domain.Entry{legacyEntry(), sampleEntry()}, Page: 2, PageSize: 2, TotalItems: 7}
	r := exportCall(t, staticExporter(page, nil), "page=2&page_size=2", entriesClaims)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d: %s", r.status, r.body)
	}
	if r.headers["Cache-Control"] != "no-store" || r.headers["Content-Type"] != "application/json" {
		t.Errorf("headers = %v", r.headers)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Page  map[string]any   `json:"page"`
	}
	if err := json.Unmarshal([]byte(r.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Page["page"] != float64(2) || body.Page["page_size"] != float64(2) || body.Page["total_items"] != float64(7) {
		t.Errorf("page = %v", body.Page)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %d, want 2 in the order the service gave them", len(body.Items))
	}
	assertLegacyEntry(t, body.Items[0])
	// Start and End are in the export (FR-IO-04), with hours, group, clients, on-site, notes and status.
	assertTimedEntry(t, body.Items[1])
}

func TestExportEntries_EmptyLedgerIsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	r := exportCall(t, staticExporter(entries.Page{Page: 1, PageSize: 50}, nil), "", entriesClaims)
	if r.status != http.StatusOK || !strings.Contains(r.body, `"items":[]`) || !strings.Contains(r.body, `"total_items":0`) {
		t.Fatalf("status %d body %s", r.status, r.body)
	}
}

func TestExportEntries_ResponseMatchesTheSpec(t *testing.T) {
	t.Parallel()
	spec := loadSpec(t)
	specRouter, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	r := exportCall(t, staticExporter(entries.Page{Items: []domain.Entry{legacyEntry(), sampleEntry()}, Page: 1, PageSize: 50, TotalItems: 2}, nil), "", entriesClaims)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test"+BasePath+"/export/entries", nil)
	if err != nil {
		t.Fatal(err)
	}
	route, params, err := specRouter.FindRoute(req)
	if err != nil {
		t.Fatalf("GET /export/entries is not in the spec: %v", err)
	}
	header := http.Header{}
	for k, v := range r.headers {
		header.Set(k, v)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 r.status,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader([]byte(r.body))),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(t.Context(), input); err != nil {
		t.Errorf("response does not match the spec: %v\n%s", err, r.body)
	}
}

func TestExportEntries_PassesPagingToTheServiceAsTheActor(t *testing.T) {
	t.Parallel()
	var got entries.ExportRequest
	var actor domain.Actor
	svc := exporter{export: func(_ context.Context, a domain.Actor, r entries.ExportRequest) (entries.Page, error) {
		got, actor = r, a
		return entries.Page{}, nil
	}}
	if r := exportCall(t, svc, "page=3&page_size=25", entriesClaims); r.status != http.StatusOK {
		t.Fatal(r.body)
	}
	if actor.ID != entriesActor.ID {
		t.Errorf("service saw actor %q", actor.ID)
	}
	if got.Page == nil || *got.Page != 3 || got.PageSize == nil || *got.PageSize != 25 {
		t.Errorf("request = %+v", got)
	}
}

func TestExportEntries_OmittedParametersStayNil(t *testing.T) {
	t.Parallel()
	var got entries.ExportRequest
	svc := exporter{export: func(_ context.Context, _ domain.Actor, r entries.ExportRequest) (entries.Page, error) {
		got = r
		return entries.Page{}, nil
	}}
	if r := exportCall(t, svc, "", entriesClaims); r.status != http.StatusOK {
		t.Fatal(r.body)
	}
	if got.Page != nil || got.PageSize != nil {
		t.Errorf("request = %+v, want both nil", got)
	}
}

func TestExportEntries_IgnoresListFiltersItDoesNotDefine(t *testing.T) {
	t.Parallel()
	// An export is the whole ledger: a filter in the query string must not reach the service as one.
	var calls int
	svc := exporter{export: func(context.Context, domain.Actor, entries.ExportRequest) (entries.Page, error) {
		calls++
		return entries.Page{}, nil
	}}
	r := exportCall(t, svc, "client=A.B.&group=direct&user_id=00000000-0000-4000-8000-0000000000ff", entriesClaims)
	if r.status != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, service calls = %d: %s", r.status, calls, r.body)
	}
}

func TestExportEntries_MalformedPagingIsA400BeforeTheService(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]string{
		"page not numeric": "page=one",
		"page size float":  "page_size=1.5",
		"repeated page":    "page=1&page=2",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := exporter{export: func(context.Context, domain.Actor, entries.ExportRequest) (entries.Page, error) {
				t.Error("the service ran for a bad query")
				return entries.Page{}, nil
			}}
			response, _ := entriesCallAt(t, svc, "/export/entries", query, entriesClaims)
			assertProblem(t, response, http.StatusBadRequest, ProblemValidation)
		})
	}
}

// The range checks (page size 1 to 200, page 1 to 100000) are the service's, so the cap is tested
// there and end to end in internal/service/entries/export_store_test.go.
func TestExportEntries_ValidationErrorsBecome400WithFieldsAndNoEchoedValues(t *testing.T) {
	t.Parallel()
	verr := domain.NewValidationError(domain.FieldError{Field: "page_size", Message: "must be between 1 and 200"})
	response, sink := entriesCallAt(t, staticExporter(entries.Page{}, verr), "/export/entries", "page_size=77", entriesClaims)
	problem := assertProblem(t, response, http.StatusBadRequest, ProblemValidation)
	if len(problem.Errors) != 1 || problem.Errors[0].Field != "page_size" {
		t.Errorf("errors = %+v", problem.Errors)
	}
	if strings.Contains(sink.buf.String(), "page_size=77") || strings.Contains(response.Body, "77") {
		t.Error("the submitted value was echoed or logged")
	}
}

func TestExportEntries_FailuresAreRedactedProblems(t *testing.T) {
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
			r := exportCall(t, staticExporter(entries.Page{}, tt.err), "", entriesClaims)
			if r.status != tt.status || r.headers["Content-Type"] != "application/problem+json" {
				t.Fatalf("status %d type %q", r.status, r.headers["Content-Type"])
			}
			for _, secret := range []string{"secret-password", "pseudonym@example.test", "SQL"} {
				if strings.Contains(r.body, secret) || strings.Contains(r.logs, secret) {
					t.Errorf("%q leaked", secret)
				}
			}
		})
	}
}

func TestExportEntries_RequiresAnAuthorizedCaller(t *testing.T) {
	t.Parallel()
	svc := exporter{export: func(context.Context, domain.Actor, entries.ExportRequest) (entries.Page, error) {
		t.Error("the service ran without an authorizer context")
		return entries.Page{}, nil
	}}
	response, _ := entriesCallAt(t, svc, "/export/entries", "", nil)
	assertProblem(t, response, http.StatusUnauthorized, ProblemUnauthorized)
}

func TestExportEntries_StoredIDsThatAreNotUUIDsFailClosed(t *testing.T) {
	t.Parallel()
	bad := sampleEntry()
	bad.Category.ID = "not-a-uuid"
	response, _ := entriesCallAt(t, staticExporter(entries.Page{Items: []domain.Entry{bad}, Page: 1, PageSize: 50, TotalItems: 1}, nil), "/export/entries", "", entriesClaims)
	assertProblem(t, response, http.StatusInternalServerError, ProblemInternal)
}
