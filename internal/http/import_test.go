package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

// S4-04 handler tests: the real router, middleware (body limit, Idempotency-Key) and generated strict
// server run against a fake import service, and responses are validated against api/openapi.yaml.
// Uploads travel as API Gateway delivers them: a base64 multipart body. Names and notes are fictional
// pseudonyms (CLAUDE.md §0.7). The service itself, and the endpoint over PostgreSQL, are tested in
// internal/service/importexport.

const importPathURL = "/api/v1/import/markdown"

// fakeImports records what the handler passed on and answers with the configured result.
type fakeImports struct {
	mu     sync.Mutex
	calls  int
	actor  domain.Actor
	got    []byte
	report importexport.Report
	err    error
}

func (f *fakeImports) Import(_ context.Context, a domain.Actor, r io.Reader) (importexport.Report, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return importexport.Report{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.actor, f.got = f.calls+1, a, data
	return f.report, f.err
}

func (f *fakeImports) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// upload describes a multipart body. A part's content type of "-" leaves the header out.
type upload struct {
	parts []uploadPartSpec
}

type uploadPartSpec struct {
	name, filename, contentType string
	content                     []byte
}

func fileUpload(content string) upload {
	return upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.md", contentType: "text/markdown", content: []byte(content)}}}
}

// encode returns the multipart body and its Content-Type header, with the given boundary.
func (u upload) encode(t *testing.T, boundary string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		t.Fatal(err)
	}
	for _, p := range u.parts {
		h := textproto.MIMEHeader{}
		disposition := `form-data; name="` + p.name + `"`
		if p.filename != "" {
			disposition += `; filename="` + p.filename + `"`
		}
		h.Set("Content-Disposition", disposition)
		if p.contentType != "-" {
			h.Set("Content-Type", p.contentType)
		}
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(p.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), w.FormDataContentType()
}

type importHarness struct {
	t       *testing.T
	svc     *fakeImports
	handler func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)
	c       *clientsHarness // For the spec check helpers.
	// path is the route requests go to; the import route unless a test changes it.
	path string
}

func newImportHarness(t *testing.T) *importHarness {
	t.Helper()
	svc := &fakeImports{}
	store := newMemIdempotency()
	deps := testDeps(&logSink{})
	deps.MaxBodyBytes = 4096 // The import route has its own, larger limit.
	deps.Imports = svc
	deps.Idempotency = store
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &importHarness{t: t, svc: svc, handler: apigw.Handler(h, ContextWithRequestID), c: &clientsHarness{t: t, spec: loadSpec(t)}, path: importPathURL}
}

// post sends the upload as an authenticated caller; headers may add or override request headers.
func (h *importHarness) post(u upload, boundary string, headers map[string]string) (events.APIGatewayV2HTTPResponse, string) {
	h.t.Helper()
	body, contentType := u.encode(h.t, boundary)
	return h.postRaw(body, contentType, headers, map[string]string{"sub": "trusted-sub"}), body
}

func (h *importHarness) postRaw(body, contentType string, headers, claims map[string]string) events.APIGatewayV2HTTPResponse {
	h.t.Helper()
	e := events.APIGatewayV2HTTPRequest{
		RawPath: h.path, Body: base64.StdEncoding.EncodeToString([]byte(body)), IsBase64Encoded: true,
		Headers: map[string]string{"content-type": contentType, "idempotency-key": testKey},
	}
	for k, v := range headers {
		if v == "" {
			delete(e.Headers, k)
			continue
		}
		e.Headers[k] = v
	}
	e.RequestContext.HTTP.Method = http.MethodPost
	if claims != nil {
		e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: claims}}
	}
	res, err := h.handler(h.t.Context(), e)
	if err != nil {
		h.t.Fatalf("handler: %v", err)
	}
	return res
}

func (h *importHarness) wantProblem(res events.APIGatewayV2HTTPResponse, status int, problemType ProblemType) map[string]any {
	h.t.Helper()
	if res.StatusCode != status {
		h.t.Fatalf("status = %d, want %d (body %s)", res.StatusCode, status, res.Body)
	}
	p := problemOf(h.t, res)
	if p["type"] != string(problemType) {
		h.t.Errorf("problem type = %v, want %s", p["type"], problemType)
	}
	h.c.checkSpec(http.MethodPost, importPathURL, "", res)
	return p
}

var sampleReport = importexport.Report{
	TotalRows: 3, ImportedCount: 1, NeedsTimeCount: 1,
	MergedClients: []importexport.MergedClient{{Client: domain.Client{ID: testClient1, Name: "Pseudonym A"}, Created: true}},
	SkippedRows: []importexport.SkippedRow{
		{Row: 2, Reason: importexport.ReasonDuplicate, Message: "Same date, category, hours, notes and clients as an existing entry."},
		{Row: 3, Reason: importexport.ReasonUnknownCategory, Field: "category", Message: "The category is not one of the service categories."},
	},
}

func TestImport_ReturnsTheReportForTheCallersOwnUpload(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	h.svc.report = sampleReport
	ledger := "# Clinical Hours Ledger\n\n## Entries\n| Date | Group | Category | Client | Hours | On-site | Notes |\n"

	res, _ := h.post(fileUpload(ledger), "b1", nil)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.StatusCode, res.Body)
	}
	if h.svc.actor.ID != testUserID {
		t.Errorf("service actor = %q, want the authenticated user %q", h.svc.actor.ID, testUserID)
	}
	if string(h.svc.got) != ledger {
		t.Errorf("service got %q, want the uploaded file", h.svc.got)
	}
	checkSampleReport(t, res.Body)
	if got := res.Headers["Cache-Control"]; got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	h.c.checkSpec(http.MethodPost, importPathURL, "", res)
}

// checkSampleReport checks that body is sampleReport as the contract spells it.
func checkSampleReport(t *testing.T, body string) {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatal(err)
	}
	if report["total_rows"] != float64(3) || report["imported_count"] != float64(1) || report["needs_time_count"] != float64(1) || report["skipped_count"] != float64(2) {
		t.Errorf("counts = %v", report)
	}
	skipped := report["skipped_rows"].([]any)
	if first := skipped[0].(map[string]any); first["reason"] != "duplicate" || first["row"] != float64(2) || first["field"] != nil {
		t.Errorf("first skipped row = %v", first)
	}
	if second := skipped[1].(map[string]any); second["reason"] != "unknown-category" || second["field"] != "category" {
		t.Errorf("second skipped row = %v", second)
	}
	merged := report["merged_clients"].([]any)[0].(map[string]any)
	if merged["created"] != true || merged["client"].(map[string]any)["name"] != "Pseudonym A" {
		t.Errorf("merged client = %v", merged)
	}
}

func TestImport_AReportWithNothingSkippedHasEmptyListsNotNull(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	res, _ := h.post(fileUpload("x"), "b1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body %s)", res.StatusCode, res.Body)
	}
	if !strings.Contains(res.Body, `"skipped_rows":[]`) || !strings.Contains(res.Body, `"merged_clients":[]`) {
		t.Errorf("empty lists must be [], got %s", res.Body)
	}
	h.c.checkSpec(http.MethodPost, importPathURL, "", res)
}

func TestImport_ARetryWithTheSameKeyReplaysTheReportWithoutImportingAgain(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	h.svc.report = sampleReport

	// A client builds a new multipart boundary for every attempt, so the bytes differ.
	first, _ := h.post(fileUpload("same file"), "boundary-one", nil)
	second, _ := h.post(fileUpload("same file"), "boundary-two", nil)

	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusOK {
		t.Fatalf("statuses = %d, %d (bodies %s | %s)", first.StatusCode, second.StatusCode, first.Body, second.Body)
	}
	if second.Body != first.Body {
		t.Errorf("replayed body differs:\n first %s\nsecond %s", first.Body, second.Body)
	}
	if second.Headers["Idempotency-Replayed"] != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", second.Headers["Idempotency-Replayed"])
	}
	if first.Headers["Idempotency-Replayed"] != "" {
		t.Error("the first response must not be marked as a replay")
	}
	if n := h.svc.callCount(); n != 1 {
		t.Errorf("import ran %d times, want 1", n)
	}
}

func TestImport_TheSameKeyWithADifferentFileIsAConflict(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	if res, _ := h.post(fileUpload("first file"), "b1", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", res.StatusCode)
	}

	res, _ := h.post(fileUpload("another file"), "b1", nil)

	h.wantProblem(res, http.StatusConflict, ProblemIdempotencyKeyReused)
	if n := h.svc.callCount(); n != 1 {
		t.Errorf("import ran %d times, want 1", n)
	}
}

func TestImport_AFailedImportIsNotStoredSoTheKeyCanRetry(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	h.svc.err = domain.ErrConflict
	res, _ := h.post(fileUpload("file"), "b1", nil)
	h.wantProblem(res, http.StatusConflict, ProblemConflict)

	h.svc.err = nil
	res, _ = h.post(fileUpload("file"), "b2", nil)

	if res.StatusCode != http.StatusOK || res.Headers["Idempotency-Replayed"] != "" {
		t.Fatalf("retry = %d replayed=%q (body %s), want a fresh 200", res.StatusCode, res.Headers["Idempotency-Replayed"], res.Body)
	}
	if n := h.svc.callCount(); n != 2 {
		t.Errorf("import ran %d times, want 2", n)
	}
}

func TestImport_RequiresAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	for name, key := range map[string]string{"missing": "", "not a UUID": "not-a-uuid"} {
		t.Run(name, func(_ *testing.T) {
			res, _ := h.post(fileUpload("file"), "b1", map[string]string{"idempotency-key": key})
			h.wantProblem(res, http.StatusBadRequest, ProblemValidation)
		})
	}
	if n := h.svc.callCount(); n != 0 {
		t.Errorf("import ran %d times, want 0", n)
	}
}

func TestImport_RequiresSignIn(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	body, contentType := fileUpload("file").encode(t, "b1")
	res := h.postRaw(body, contentType, nil, nil)
	h.wantProblem(res, http.StatusUnauthorized, ProblemUnauthorized)
	if n := h.svc.callCount(); n != 0 {
		t.Errorf("import ran %d times, want 0", n)
	}
}

func TestImport_Limits(t *testing.T) {
	t.Parallel()
	const mib = 1 << 20

	t.Run("a file of exactly 1 MiB is passed to the service", func(t *testing.T) {
		t.Parallel()
		h := newImportHarness(t)
		res, _ := h.post(fileUpload(strings.Repeat("a", mib)), "b1", nil)
		if res.StatusCode != http.StatusOK || len(h.svc.got) != mib {
			t.Fatalf("status = %d, service got %d bytes (body %.200s)", res.StatusCode, len(h.svc.got), res.Body)
		}
	})
	t.Run("a file one byte over 1 MiB is 413 body-too-large", func(t *testing.T) {
		t.Parallel()
		h := newImportHarness(t)
		res, _ := h.post(fileUpload(strings.Repeat("a", mib+1)), "b1", nil)
		h.wantProblem(res, http.StatusRequestEntityTooLarge, ProblemBodyTooLarge)
		if n := h.svc.callCount(); n != 0 {
			t.Errorf("import ran %d times, want 0", n)
		}
	})
	t.Run("a body far over the limit is 413 before it is read", func(t *testing.T) {
		t.Parallel()
		h := newImportHarness(t)
		res, _ := h.post(fileUpload(strings.Repeat("a", 3*mib)), "b1", nil)
		p := h.wantProblem(res, http.StatusRequestEntityTooLarge, ProblemBodyTooLarge)
		if detail, _ := p["detail"].(string); strings.Contains(detail, "aaaa") {
			t.Errorf("detail echoes the upload: %.100s", detail)
		}
		if n := h.svc.callCount(); n != 0 {
			t.Errorf("import ran %d times, want 0", n)
		}
	})
	t.Run("too many rows is 413 import-too-many-rows", func(t *testing.T) {
		t.Parallel()
		h := newImportHarness(t)
		h.svc.err = importexport.ErrTooManyRows
		res, _ := h.post(fileUpload("rows"), "b1", nil)
		h.wantProblem(res, http.StatusRequestEntityTooLarge, ProblemImportTooManyRows)
	})
	t.Run("the other routes keep the small default limit", func(t *testing.T) {
		t.Parallel()
		h := newImportHarness(t)
		// Well under the import route's limit, well over this harness's default of 4096 bytes.
		h.path = "/api/v1/clients"
		res := h.postRaw(`{"name":"`+strings.Repeat("a", 20000)+`"}`, "application/json", nil, map[string]string{"sub": "trusted-sub"})
		h.wantProblem(res, http.StatusRequestEntityTooLarge, ProblemBodyTooLarge)
		if n := h.svc.callCount(); n != 0 {
			t.Errorf("import ran %d times, want 0", n)
		}
	})
}

func TestImport_ARequestThatIsNotAMarkdownFileIsRefused(t *testing.T) {
	t.Parallel()
	pngMagic := string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	tests := []struct {
		name   string
		upload upload
		field  string
	}{
		{"PNG image", upload{parts: []uploadPartSpec{{name: "file", filename: "chart.png", contentType: "image/png", content: []byte(pngMagic)}}}, "file"},
		{"JSON", upload{parts: []uploadPartSpec{{name: "file", filename: "data.json", contentType: "application/json", content: []byte(`{"a":1}`)}}}, "file"},
		{"generic binary", upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.md", contentType: "application/octet-stream", content: []byte("# Ledger")}}}, "file"},
		{"HTML", upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.html", contentType: "text/html", content: []byte("<script>alert(1)</script>")}}}, "file"},
		{"unparsable content type", upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.md", contentType: "text/;;", content: []byte("# Ledger")}}}, "file"},
		{"no file part", upload{}, "file"},
		{"the part has another name", upload{parts: []uploadPartSpec{{name: "ledger", filename: "ledger.md", contentType: "text/markdown", content: []byte("# Ledger")}}}, "ledger"},
		{"an extra part", upload{parts: []uploadPartSpec{
			{name: "file", filename: "ledger.md", contentType: "text/markdown", content: []byte("# Ledger")},
			{name: "user_id", contentType: "-", content: []byte("someone-else")},
		}}, "user_id"},
		{"a part name that is not safe to repeat", upload{parts: []uploadPartSpec{{name: "<img src=x>", contentType: "text/markdown", content: []byte("x")}}}, "body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newImportHarness(t)
			res, _ := h.post(tt.upload, "b1", nil)
			p := h.wantProblem(res, http.StatusBadRequest, ProblemValidation)
			errs, _ := p["errors"].([]any)
			if len(errs) != 1 || errs[0].(map[string]any)["field"] != tt.field {
				t.Errorf("errors = %v, want one for %q", p["errors"], tt.field)
			}
			if strings.Contains(res.Body, "someone-else") || strings.Contains(res.Body, "alert(1)") || strings.Contains(res.Body, "chart.png") {
				t.Errorf("the problem echoes the upload: %s", res.Body)
			}
			if n := h.svc.callCount(); n != 0 {
				t.Errorf("import ran %d times, want 0", n)
			}
		})
	}
}

func TestImport_APartWithoutAContentTypeIsPlainText(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	res, _ := h.post(upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.md", contentType: "-", content: []byte("# Ledger")}}}, "b1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body %s)", res.StatusCode, res.Body)
	}
}

func TestImport_ContentTypeParametersAndCaseAreIgnored(t *testing.T) {
	t.Parallel()
	for _, ct := range []string{"text/markdown; charset=utf-8", "Text/Plain", "text/markdown;variant=GFM"} {
		h := newImportHarness(t)
		res, _ := h.post(upload{parts: []uploadPartSpec{{name: "file", filename: "ledger.md", contentType: ct, content: []byte("# Ledger")}}}, "b1", nil)
		if res.StatusCode != http.StatusOK {
			t.Errorf("content type %q: status = %d (body %s)", ct, res.StatusCode, res.Body)
		}
	}
}

func TestImport_ABodyThatIsNotMultipartIsMalformed(t *testing.T) {
	t.Parallel()
	h := newImportHarness(t)
	claims := map[string]string{"sub": "trusted-sub"}
	for name, tt := range map[string]struct{ body, contentType string }{
		"JSON":                       {`{"file":"x"}`, "application/json"},
		"plain text":                 {"# Ledger", "text/markdown"},
		"multipart with no boundary": {"--x\r\n\r\n", "multipart/form-data"},
		"truncated multipart":        {"--b1\r\nContent-Disposition: form-data; name=\"file\"\r\nContent-Type: text/markdown\r\n\r\nabc", "multipart/form-data; boundary=b1"},
	} {
		t.Run(name, func(_ *testing.T) {
			res := h.postRaw(tt.body, tt.contentType, nil, claims)
			h.wantProblem(res, http.StatusBadRequest, ProblemValidation)
		})
	}
	if n := h.svc.callCount(); n != 0 {
		t.Errorf("import ran %d times, want 0", n)
	}
}

func TestImport_MapsTheServiceErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		err         error
		status      int
		problemType ProblemType
	}{
		{"file not UTF-8", importexport.ErrNotText, http.StatusBadRequest, ProblemValidation},
		{"no entries table", importexport.ErrNoEntriesTable, http.StatusBadRequest, ProblemValidation},
		{"file too large", importexport.ErrTooLarge, http.StatusRequestEntityTooLarge, ProblemBodyTooLarge},
		{"wrapped refusal", errors.Join(errors.New("context"), importexport.ErrTooManyRows), http.StatusRequestEntityTooLarge, ProblemImportTooManyRows},
		{"concurrent clash", domain.ErrConflict, http.StatusConflict, ProblemConflict},
		{"not allowed", domain.ErrForbidden, http.StatusForbidden, ProblemForbidden},
		{"storage failure", errors.New("pq: connection refused at 10.0.0.12"), http.StatusInternalServerError, ProblemInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newImportHarness(t)
			h.svc.err = tt.err
			res, _ := h.post(fileUpload("# Ledger"), "b1", nil)
			p := h.wantProblem(res, tt.status, tt.problemType)
			if strings.Contains(res.Body, "10.0.0.12") || strings.Contains(res.Body, "connection refused") {
				t.Errorf("the problem leaks the internal error: %s", res.Body)
			}
			if tt.status == http.StatusBadRequest {
				if errs, _ := p["errors"].([]any); len(errs) != 1 || errs[0].(map[string]any)["field"] != "file" {
					t.Errorf("errors = %v, want one for file", p["errors"])
				}
			}
		})
	}
}

// The hash that identifies a retry must not depend on the multipart framing.
func TestMultipartDigest(t *testing.T) {
	t.Parallel()
	digest := func(u upload, boundary string) []byte {
		body, ct := u.encode(t, boundary)
		d, ok := multipartDigest(ct, []byte(body))
		if !ok {
			t.Fatal("not hashed as multipart")
		}
		return d
	}
	base := fileUpload("same file")
	if !bytes.Equal(digest(base, "aaa"), digest(base, "bbb")) {
		t.Error("the boundary changes the digest")
	}
	renamed := upload{parts: []uploadPartSpec{{name: "file", filename: "other-name.md", contentType: "text/plain", content: []byte("same file")}}}
	if !bytes.Equal(digest(base, "aaa"), digest(renamed, "aaa")) {
		t.Error("the file name or content type changes the digest")
	}
	for name, other := range map[string]upload{
		"content": fileUpload("another file"),
		"name":    {parts: []uploadPartSpec{{name: "ledger", contentType: "text/markdown", content: []byte("same file")}}},
		"split": {parts: []uploadPartSpec{
			{name: "file", contentType: "text/markdown", content: []byte("same ")},
			{name: "file", contentType: "text/markdown", content: []byte("file")},
		}},
	} {
		if bytes.Equal(digest(base, "aaa"), digest(other, "aaa")) {
			t.Errorf("a different %s has the same digest", name)
		}
	}
	if _, ok := multipartDigest("application/json", []byte(`{}`)); ok {
		t.Error("JSON was hashed as multipart")
	}
	if _, ok := multipartDigest("multipart/form-data; boundary=b", []byte("--b\r\nbroken")); ok {
		t.Error("a broken multipart body was hashed as multipart")
	}
}
