package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/idempotency"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// S2-09 middleware tests: the real router and generated server run against a fake entries service
// and an in-memory store with the semantics of the PostgreSQL one (the PostgreSQL store has its own
// end-to-end tests in internal/service/entries). Names and notes are fictional (CLAUDE.md §0.7).

// memIdempotency keeps claims in memory, scoped to the user like the real store.
type memIdempotency struct {
	mu   sync.Mutex
	rows map[string]*memRow

	claimErr, completeErr, releaseErr error
	claims, completes, releases       int
}

type memRow struct {
	hash     []byte
	response *idempotency.Response // nil while in progress
}

func newMemIdempotency() *memIdempotency { return &memIdempotency{rows: map[string]*memRow{}} }

func (m *memIdempotency) Claim(_ context.Context, userID, key string, hash []byte) (idempotency.Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claims++
	if m.claimErr != nil {
		return idempotency.Claim{}, m.claimErr
	}
	row, ok := m.rows[userID+"/"+key]
	switch {
	case !ok:
		m.rows[userID+"/"+key] = &memRow{hash: hash}
		return idempotency.Claim{Outcome: idempotency.Acquired}, nil
	case !bytes.Equal(row.hash, hash):
		return idempotency.Claim{Outcome: idempotency.Mismatch}, nil
	case row.response != nil:
		return idempotency.Claim{Outcome: idempotency.Replay, Response: *row.response}, nil
	default:
		return idempotency.Claim{Outcome: idempotency.InProgress}, nil
	}
}

func (m *memIdempotency) Complete(_ context.Context, userID, key string, _ []byte, r idempotency.Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completes++
	if m.completeErr != nil {
		return m.completeErr
	}
	m.rows[userID+"/"+key].response = &r
	return nil
}

func (m *memIdempotency) Release(_ context.Context, userID, key string, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases++
	if m.releaseErr != nil {
		return m.releaseErr
	}
	delete(m.rows, userID+"/"+key)
	return nil
}

// panickingEntries makes the create handler panic after the claim was taken.
type panickingEntries struct{ fakeEntries }

func (*panickingEntries) Create(context.Context, domain.Actor, entries.Input) (domain.Entry, error) {
	panic("boom")
}

func newIdempotentHarness(t *testing.T) (*clientsHarness, *fakeEntries, *memIdempotency) {
	t.Helper()
	c, svc := newEntriesHarness(t)
	c.svc = &fakeClients{client: domain.Client{ID: testClient1, Name: "A.B."}}
	store := newMemIdempotency()
	rebuild(t, c, svc, store)
	return c, svc, store
}

// rebuild swaps the harness handler for one wired to store and entries.
func rebuild(t *testing.T, c *clientsHarness, entriesSvc EntryService, store idempotency.Store) {
	t.Helper()
	deps := testDeps(&logSink{})
	deps.MaxBodyBytes = 8192
	deps.Entries = entriesSvc
	deps.Clients = c.svc
	deps.Idempotency = store
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	c.handler = apigw.Handler(h, ContextWithRequestID)
}

func countCalls(svc *fakeEntries, name string) int {
	n := 0
	for _, c := range svc.calls {
		if c == name {
			n++
		}
	}
	return n
}

func TestIdempotency_SameKeyAndBodyReplaysTheFirstResponse(t *testing.T) {
	t.Parallel()
	c, svc, store := newIdempotentHarness(t)
	first := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	second := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())

	if first.StatusCode != http.StatusCreated || second.StatusCode != http.StatusCreated {
		t.Fatalf("statuses = %d, %d (bodies %s | %s)", first.StatusCode, second.StatusCode, first.Body, second.Body)
	}
	if second.Body != first.Body {
		t.Errorf("replayed body differs:\n first %s\nsecond %s", first.Body, second.Body)
	}
	if n := countCalls(svc, "create"); n != 1 {
		t.Errorf("create ran %d times, want 1", n)
	}
	if second.Headers["Idempotency-Replayed"] != "true" || first.Headers["Idempotency-Replayed"] != "" {
		t.Errorf("replay header: first %q, second %q", first.Headers["Idempotency-Replayed"], second.Headers["Idempotency-Replayed"])
	}
	if second.Headers["Content-Type"] != "application/json" || second.Headers["Cache-Control"] != "no-store" {
		t.Errorf("replay headers = %v", second.Headers)
	}
	c.checkSpec(http.MethodPost, "/api/v1/entries", entryBody, second)
	if store.completes != 1 || store.releases != 0 {
		t.Errorf("completes %d, releases %d", store.completes, store.releases)
	}
}

func TestIdempotency_ReorderedOrReformattedJSONIsTheSameRequest(t *testing.T) {
	t.Parallel()
	c, svc, _ := newIdempotentHarness(t)
	compact := `{"session_date":"2026-10-12","start_time":"09:00","end_time":"10:15","category_id":"` + testCategoryID + `"}`
	spaced := "{\n  \"category_id\": \"" + testCategoryID + "\",\n  \"end_time\": \"10:15\",\n  \"start_time\": \"09:00\",\n  \"session_date\": \"2026-10-12\"\n}"
	if res := c.send(http.MethodPost, "/api/v1/entries", compact, keyed()); res.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d %s", res.StatusCode, res.Body)
	}
	res := c.send(http.MethodPost, "/api/v1/entries", spaced, keyed())
	if res.StatusCode != http.StatusCreated || res.Headers["Idempotency-Replayed"] != "true" {
		t.Fatalf("reformatted retry = %d %s, headers %v", res.StatusCode, res.Body, res.Headers)
	}
	if n := countCalls(svc, "create"); n != 1 {
		t.Errorf("create ran %d times, want 1", n)
	}
}

func TestIdempotency_SameKeyWithADifferentBodyIs409(t *testing.T) {
	t.Parallel()
	c, svc, _ := newIdempotentHarness(t)
	if res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed()); res.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d %s", res.StatusCode, res.Body)
	}
	other := strings.Replace(entryBody, `"end_time":"10:15"`, `"end_time":"11:00"`, 1)
	res := c.send(http.MethodPost, "/api/v1/entries", other, keyed())
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if p := problemOf(t, res); p["type"] != string(ProblemIdempotencyKeyReused) {
		t.Errorf("type = %v", p["type"])
	}
	if strings.Contains(res.Body, "11:00") {
		t.Error("the problem echoes the submitted body")
	}
	if n := countCalls(svc, "create"); n != 1 {
		t.Errorf("create ran %d times, want 1", n)
	}
	c.checkSpec(http.MethodPost, "/api/v1/entries", other, res)
}

func TestIdempotency_TheSameKeyOnAnotherEndpointIs409(t *testing.T) {
	t.Parallel()
	c, _, _ := newIdempotentHarness(t)
	if res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed()); res.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d %s", res.StatusCode, res.Body)
	}
	res := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B."}`, keyed())
	if res.StatusCode != http.StatusConflict || problemOf(t, res)["type"] != string(ProblemIdempotencyKeyReused) {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
}

func TestIdempotency_ARequestStillRunningIs409WithRetryAfter(t *testing.T) {
	t.Parallel()
	c, svc, store := newIdempotentHarness(t)
	if _, err := store.Claim(t.Context(), testUserID, testKey, requestHash(mustRequest(t, "/api/v1/entries"), []byte(entryBody))); err != nil {
		t.Fatal(err)
	}
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusConflict || problemOf(t, res)["type"] != string(ProblemIdempotencyInProgress) {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if res.Headers["Retry-After"] != "1" {
		t.Errorf("Retry-After = %q", res.Headers["Retry-After"])
	}
	if len(svc.calls) != 0 {
		t.Errorf("the service ran while the key was claimed: %v", svc.calls)
	}
	c.checkSpec(http.MethodPost, "/api/v1/entries", entryBody, res)
}

func TestIdempotency_AFailedRequestReleasesTheKeySoARetryRunsAgain(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"validation", domain.NewValidationError(domain.FieldError{Field: "notes", Message: "is too long"}), http.StatusBadRequest},
		{"overlap", &domain.EntryOverlapError{}, http.StatusConflict},
		{"server error", errors.New("database exploded"), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, svc, store := newIdempotentHarness(t)
			svc.err = tt.err
			if res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed()); res.StatusCode != tt.status {
				t.Fatalf("first = %d %s", res.StatusCode, res.Body)
			}
			svc.err = nil // The user fixed the problem and retries with the same key.
			res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
			if res.StatusCode != http.StatusCreated || res.Headers["Idempotency-Replayed"] != "" {
				t.Fatalf("retry = %d %s", res.StatusCode, res.Body)
			}
			if n := countCalls(svc, "create"); n != 2 {
				t.Errorf("create ran %d times, want 2", n)
			}
			if store.releases != 1 || store.completes != 1 {
				t.Errorf("releases %d, completes %d", store.releases, store.completes)
			}
		})
	}
}

func TestIdempotency_APanicReleasesTheKey(t *testing.T) {
	t.Parallel()
	c, _, store := newIdempotentHarness(t)
	rebuild(t, c, &panickingEntries{fakeEntries{entry: sampleEntry()}}, store)
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if store.releases != 1 || len(store.rows) != 0 {
		t.Errorf("releases %d, rows %d: the key stayed claimed", store.releases, len(store.rows))
	}
}

func TestIdempotency_AStoreFailureStopsTheCreate(t *testing.T) {
	t.Parallel()
	c, svc, store := newIdempotentHarness(t)
	store.claimErr = errors.New("database unavailable")
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if len(svc.calls) != 0 {
		t.Errorf("the create ran without a claim: %v", svc.calls)
	}
	if strings.Contains(res.Body, "database unavailable") {
		t.Error("the problem leaks the storage error")
	}
}

func TestIdempotency_ACreateStillSucceedsWhenItsResponseCannotBeStored(t *testing.T) {
	t.Parallel()
	c, svc, store := newIdempotentHarness(t)
	store.completeErr = idempotency.ErrClaimLost
	res := c.send(http.MethodPost, "/api/v1/entries", entryBody, keyed())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if n := countCalls(svc, "create"); n != 1 {
		t.Errorf("create ran %d times, want 1", n)
	}
}

func TestIdempotency_MissingOrMalformedKeyIs400WithoutTouchingTheStore(t *testing.T) {
	t.Parallel()
	for name, headers := range map[string]map[string]string{
		"missing":   nil,
		"malformed": {"Idempotency-Key": "not-a-uuid"},
		"empty":     {"Idempotency-Key": ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, svc, store := newIdempotentHarness(t)
			res := c.send(http.MethodPost, "/api/v1/entries", entryBody, headers)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
			if store.claims != 0 || len(svc.calls) != 0 {
				t.Errorf("claims %d, service calls %v", store.claims, svc.calls)
			}
		})
	}
}

func TestIdempotency_AnInvalidBodyNeverClaimsTheKey(t *testing.T) {
	t.Parallel()
	c, _, store := newIdempotentHarness(t)
	res := c.send(http.MethodPost, "/api/v1/entries", `{"hours":2}`, keyed())
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if store.claims != 0 {
		t.Errorf("claims = %d", store.claims)
	}
}

func TestIdempotency_OnlyCreateOperationsUseTheStore(t *testing.T) {
	t.Parallel()
	c, _, store := newIdempotentHarness(t)
	target := "/api/v1/entries/" + testEntryID
	c.send(http.MethodGet, target, "", nil)
	c.send(http.MethodPut, target, entryBody, keyed())
	c.send(http.MethodDelete, target, "", keyed())
	c.send(http.MethodGet, "/api/v1/entries", "", nil)
	if store.claims != 0 {
		t.Errorf("claims = %d, want 0 for GET, PUT and DELETE", store.claims)
	}
}

func TestIdempotency_ClientsCreateIsCoveredToo(t *testing.T) {
	t.Parallel()
	c, _, _ := newIdempotentHarness(t)
	first := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B."}`, keyed())
	second := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B."}`, keyed())
	if first.StatusCode != http.StatusCreated || second.StatusCode != http.StatusCreated || second.Headers["Idempotency-Replayed"] != "true" {
		t.Fatalf("statuses = %d, %d; replay header %q", first.StatusCode, second.StatusCode, second.Headers["Idempotency-Replayed"])
	}
}

func TestIdempotentOperations_AreExactlyThePostsThatDeclareTheHeader(t *testing.T) {
	t.Parallel()
	got := idempotentOperations(loadSpec(t))
	want := map[string]bool{"POST /api/v1/entries": true, "POST /api/v1/clients": true}
	if len(got) != len(want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	for op := range want {
		if !got[op] {
			t.Errorf("%s is not covered", op)
		}
	}
}

func mustRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, nil)
}
