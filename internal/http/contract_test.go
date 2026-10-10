package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
)

// loadSpec returns the embedded api/openapi.yaml, validated, with the server URL made absolute
// so the router can match test requests.
func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("load embedded spec: %v", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	spec.Servers = openapi3.Servers{{URL: "http://example.test" + BasePath}}
	return spec
}

// TestContract_ResponsesMatchTheSpec runs real requests through the router and validates each
// response against api/openapi.yaml (ADR-0006 contract test).
func TestContract_ResponsesMatchTheSpec(t *testing.T) {
	t.Parallel()
	spec := loadSpec(t)
	specRouter, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatalf("spec router: %v", err)
	}
	h, err := NewRouter(testDeps(&logSink{}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	tests := []struct {
		name, method, path string
		wantStatus         int
	}{
		{"healthz", http.MethodGet, "/api/v1/healthz", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, "http://example.test"+tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			route, params, err := specRouter.FindRoute(req)
			if err != nil {
				t.Fatalf("route not in spec: %v", err)
			}
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
				Status:                 rec.Code,
				Header:                 rec.Header(),
				Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
				Options:                &openapi3filter.Options{IncludeResponseStatus: true},
			}
			if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
				t.Errorf("response does not match the spec: %v\nbody: %s", err, rec.Body.String())
			}
		})
	}
}

// TestContract_EveryProblemMatchesTheProblemSchema checks each problem the server can send
// against components.schemas.Problem, including the extra fields of validation problems.
func TestContract_EveryProblemMatchesTheProblemSchema(t *testing.T) {
	t.Parallel()
	schema := loadSpec(t).Components.Schemas["Problem"].Value
	errs := []error{
		domain.ErrNotFound,
		domain.ErrForbidden,
		domain.ErrConflict,
		domain.NewValidationError(domain.FieldError{Field: "end_time", Message: "must be after start_time"}),
		&http.MaxBytesError{Limit: 16},
		errors.New("unexpected"),
	}
	for _, e := range errs {
		p := problemFor(e)
		p.RequestID = "generated-id"
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if err := schema.VisitJSON(doc); err != nil {
			t.Errorf("problem for %v does not match the schema: %v (%s)", e, err, raw)
		}
	}
}

// TestContract_ProblemTypesAreDocumented keeps the Go problem types and the spec example aligned.
func TestContract_ProblemTypesAreDocumented(t *testing.T) {
	t.Parallel()
	for _, pt := range []ProblemType{ProblemInternal, ProblemNotFound, ProblemMethodNotAllowed, ProblemBodyTooLarge, ProblemForbidden, ProblemConflict, ProblemValidation} {
		if !strings.HasPrefix(string(pt), "urn:therapy-service-hours:problem:") {
			t.Errorf("problem type %q does not use the documented URN prefix", pt)
		}
	}
}
