package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

func TestHealthz_ReturnsVersionAsUncachedJSON(t *testing.T) {
	t.Parallel()
	h, err := NewRouter(testDeps(&logSink{}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (SEC-33)", got)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok","version":"test-version"}` {
		t.Errorf("body = %s", got)
	}
}

func TestRouter_UnknownAndWrongMethodReturnProblemJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, method, path string
		wantStatus         int
		wantType           ProblemType
	}{
		{"unknown API path", http.MethodGet, "/api/v1/x", http.StatusNotFound, ProblemNotFound},
		{"path outside the API", http.MethodGet, "/index.html", http.StatusNotFound, ProblemNotFound},
		{"wrong method", http.MethodPost, "/api/v1/healthz", http.StatusMethodNotAllowed, ProblemMethodNotAllowed},
	}
	h, err := NewRouter(testDeps(&logSink{}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q", got)
			}
			p := decodeProblem(t, rec.Body.Bytes())
			if p.Type != tt.wantType || p.Status != tt.wantStatus || p.RequestID != "generated-id" {
				t.Errorf("problem = %+v", p)
			}
		})
	}
}

func TestNewRouter_RejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := NewRouter(RouterDeps{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"logger", "version", "max body bytes", "request ID generator", "clock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		upstream string
		header   string
		want     string
	}{
		{name: "generated when missing", want: "generated-id"},
		{name: "API Gateway ID is reused", upstream: "apigw-123", want: "apigw-123"},
		{name: "client header is ignored", header: "forged", want: "generated-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux := newMux(testDeps(&logSink{}))
			var seen string
			mux.Get("/probe", func(w http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tt.upstream != "" {
				req = req.WithContext(ContextWithRequestID(req.Context(), tt.upstream))
			}
			if tt.header != "" {
				req.Header.Set(RequestIDHeader, tt.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if seen != tt.want {
				t.Errorf("handler saw request ID %q, want %q", seen, tt.want)
			}
			if got := rec.Header().Get(RequestIDHeader); got != tt.want {
				t.Errorf("%s header = %q, want %q", RequestIDHeader, got, tt.want)
			}
		})
	}
}

func TestLogging_EveryLineIsJSONWithRequestIDAndFunction(t *testing.T) {
	t.Parallel()
	sink := &logSink{}
	mux := newMux(testDeps(sink))
	mux.Get("/ok", func(w http.ResponseWriter, r *http.Request) {
		LoggerFrom(r.Context()).Info("handler log line")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Get("/fail", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errors.New("database unreachable"))
	})
	mux.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })

	for _, path := range []string{"/ok", "/fail", "/panic", "/missing"} {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	lines := sink.lines(t)
	if len(lines) < 7 {
		t.Fatalf("expected at least 7 log lines, got %d", len(lines))
	}
	for _, line := range lines {
		if line["request_id"] != "generated-id" {
			t.Errorf("log line without request_id: %v", line)
		}
		if line["function"] != "api" {
			t.Errorf("log line without function: %v", line)
		}
	}
}

func TestAccessLog_RecordsRoutePatternNotRawPath(t *testing.T) {
	t.Parallel()
	sink := &logSink{}
	mux := newMux(testDeps(sink))
	mux.Get("/people/{name}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/people/Pseudonym-A?search=Pseudonym-B", nil))

	lines := sink.lines(t)
	access := lines[len(lines)-1]
	if access["route"] != "/people/{name}" || access["status"] != float64(http.StatusNoContent) || access["method"] != "GET" {
		t.Errorf("access line = %v", access)
	}
	for _, line := range lines {
		for _, v := range line {
			if s, ok := v.(string); ok && strings.Contains(s, "Pseudonym") {
				t.Errorf("raw path or query leaked into the log (GO-64): %v", line)
			}
		}
	}
}

func TestRecover_PanicReturnsProblem500AndLogsStack(t *testing.T) {
	t.Parallel()
	sink := &logSink{}
	mux := newMux(testDeps(sink))
	mux.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("secret internal detail") })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q", got)
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("panic details leaked to the client (SEC-34): %s", rec.Body.String())
	}
	if p := decodeProblem(t, rec.Body.Bytes()); p.Type != ProblemInternal || p.RequestID != "generated-id" {
		t.Errorf("problem = %+v", p)
	}

	var panicLine map[string]any
	for _, line := range sink.lines(t) {
		if line["msg"] == "panic recovered" {
			panicLine = line
		}
	}
	if panicLine == nil {
		t.Fatal("no panic log line")
	}
	if panicLine["level"] != "ERROR" || panicLine["panic"] != "secret internal detail" {
		t.Errorf("panic line = %v", panicLine)
	}
	if stack, _ := panicLine["stack"].(string); !strings.Contains(stack, "recoverPanics") {
		t.Errorf("stack not logged: %q", stack)
	}
}

func TestRecover_ReraisesErrAbortHandler(t *testing.T) {
	t.Parallel()
	mux := newMux(testDeps(&logSink{}))
	mux.Get("/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint // Comparing the exact sentinel.
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	t.Error("expected the abort panic to propagate")
}

func TestLimitBody(t *testing.T) {
	t.Parallel()
	echo := func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			WriteError(w, r, fmt.Errorf("read body: %w", err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	tests := []struct {
		name          string
		body          string
		hideLength    bool
		wantStatus    int
		wantHandlerOK bool
	}{
		{name: "at the limit", body: strings.Repeat("a", 16), wantStatus: http.StatusNoContent},
		{name: "declared length over the limit", body: strings.Repeat("a", 17), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streamed body over the limit", body: strings.Repeat("a", 17), hideLength: true, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux := newMux(testDeps(&logSink{}))
			mux.Post("/echo", echo)
			req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(tt.body))
			if tt.hideLength {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusRequestEntityTooLarge {
				p := decodeProblem(t, rec.Body.Bytes())
				if p.Type != ProblemBodyTooLarge || !strings.Contains(p.Detail, "16 bytes") {
					t.Errorf("problem = %+v", p)
				}
			}
		})
	}
}

func TestProblemFor_MapsDomainErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   ProblemType
	}{
		{"not found (wrapped)", fmt.Errorf("get entry: %w", domain.ErrNotFound), http.StatusNotFound, ProblemNotFound},
		{"forbidden", domain.ErrForbidden, http.StatusForbidden, ProblemForbidden},
		{"conflict (wrapped)", fmt.Errorf("approve: %w", domain.ErrConflict), http.StatusConflict, ProblemConflict},
		{"validation", domain.NewValidationError(domain.FieldError{Field: "end_time", Message: "must be after start_time"}), http.StatusBadRequest, ProblemValidation},
		{"body too large", &http.MaxBytesError{Limit: 10}, http.StatusRequestEntityTooLarge, ProblemBodyTooLarge},
		{"unknown", errors.New("pq: relation users does not exist"), http.StatusInternalServerError, ProblemInternal},
		{"context cancelled", context.Canceled, http.StatusInternalServerError, ProblemInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := problemFor(tt.err)
			if p.Status != tt.wantStatus || p.Type != tt.wantType {
				t.Errorf("problemFor = %+v, want %d %s", p, tt.wantStatus, tt.wantType)
			}
		})
	}
}

func TestWriteError_LogsOnceAndHidesInternalText(t *testing.T) {
	t.Parallel()
	sink := &logSink{}
	mux := newMux(testDeps(sink))
	mux.Get("/fail", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errors.New("pq: relation users does not exist"))
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fail", nil))

	if strings.Contains(rec.Body.String(), "relation") {
		t.Errorf("internal error text leaked to the client (GO-33): %s", rec.Body.String())
	}
	failures := 0
	for _, line := range sink.lines(t) {
		if line["msg"] == "request failed" {
			failures++
			if line["level"] != "ERROR" || line["status"] != float64(http.StatusInternalServerError) {
				t.Errorf("failure line = %v", line)
			}
		}
	}
	if failures != 1 {
		t.Errorf("logged %d failure lines, want exactly 1 (GO-65)", failures)
	}
}

func TestWriteError_ValidationListsFields(t *testing.T) {
	t.Parallel()
	mux := newMux(testDeps(&logSink{}))
	mux.Get("/invalid", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, domain.NewValidationError(
			domain.FieldError{Field: "start_time", Message: "must be on a 15-minute step"},
			domain.FieldError{Field: "end_time", Message: "must be after start_time"},
		))
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invalid", nil))

	p := decodeProblem(t, rec.Body.Bytes())
	if rec.Code != http.StatusBadRequest || len(p.Errors) != 2 || p.Errors[1].Field != "end_time" {
		t.Errorf("status %d, problem %+v", rec.Code, p)
	}
}
