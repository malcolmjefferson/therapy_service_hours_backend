package entries_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
)

// S2-09 end-to-end tests: Idempotency-Key through the real router, middleware and PostgreSQL store
// against the real migrations (GO-57, GO-72). Users, clients and notes are fictional pseudonyms
// (CLAUDE.md §0.7).

const (
	keyA = "00000000-0000-4000-8000-00000000a001"
	keyB = "00000000-0000-4000-8000-00000000a002"
)

func (e *env) idempotencyRows() int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM idempotency_keys`)
}

func TestIdempotency_SameKeyTwiceCreatesOneEntry(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	body := e.body("2026-10-05", "09:00", "10:00", 1, `"notes":"first"`)

	s1, r1 := e.callWithKey(http.MethodPost, "/entries", body, keyA)
	s2, r2 := e.callWithKey(http.MethodPost, "/entries", body, keyA)

	if s1 != http.StatusCreated || s2 != http.StatusCreated {
		t.Fatalf("statuses = %d, %d (%s | %s)", s1, s2, r1, r2)
	}
	if r1 != r2 {
		t.Errorf("replay differs from the original:\n%s\n%s", r1, r2)
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM idempotency_keys WHERE state = 'completed' AND response_status = 201`); n != 1 {
		t.Errorf("completed keys = %d, want 1", n)
	}
}

func TestIdempotency_SameKeyWithADifferentPayloadIs409AndStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	if s, r := e.callWithKey(http.MethodPost, "/entries", e.body("2026-10-05", "09:00", "10:00", 1), keyA); s != http.StatusCreated {
		t.Fatalf("first = %d %s", s, r)
	}
	s, r := e.callWithKey(http.MethodPost, "/entries", e.body("2026-10-05", "13:00", "14:00", 1), keyA)
	expectProblem(t, s, r, http.StatusConflict, "idempotency-key-reused")
	if n := e.count(`SELECT count(*) FROM entries`); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
}

func TestIdempotency_KeysBelongToTheUser(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	body := e.body("2026-10-05", "09:00", "10:00", 1)

	e.actor = a
	if s, r := e.callWithKey(http.MethodPost, "/entries", body, keyA); s != http.StatusCreated {
		t.Fatalf("a = %d %s", s, r)
	}
	e.actor = b
	s, r := e.callWithKey(http.MethodPost, "/entries", body, keyA)
	if s != http.StatusCreated {
		t.Fatalf("b reusing a's key = %d %s", s, r)
	}
	if got := decode[map[string]any](t, r); got["id"] == nil {
		t.Fatalf("b got %s", r)
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 2 {
		t.Errorf("entries = %d, want one per user", n)
	}
	if n := e.idempotencyRows(); n != 2 {
		t.Errorf("idempotency rows = %d, want one per user", n)
	}
}

func TestIdempotency_ConcurrentRetriesOfOneKeyCreateExactlyOneEntry(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	body := e.body("2026-10-05", "09:00", "10:00", 1)
	const n = 8
	type result struct {
		status int
		body   string
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, res := e.callWithKey(http.MethodPost, "/entries", body, keyA)
			results[i] = result{status, res}
		}()
	}
	close(start)
	wg.Wait()

	var id string
	for _, r := range results {
		switch r.status {
		case http.StatusCreated:
			got := decode[map[string]any](t, r.body)["id"].(string)
			if id != "" && got != id {
				t.Fatalf("two different entries were returned: %s and %s", id, got)
			}
			id = got
		case http.StatusConflict:
			// A retry that arrived while the first was running is told to try again.
			expectProblem(t, r.status, r.body, http.StatusConflict, "idempotency-key-in-progress")
		default:
			t.Fatalf("unexpected result %d %s", r.status, r.body)
		}
	}
	if id == "" {
		t.Fatalf("no request succeeded: %v", results)
	}
	if got := e.count(`SELECT count(*) FROM entries`); got != 1 {
		t.Errorf("entries = %d, want 1", got)
	}
}

func TestIdempotency_AFailedCreateReleasesTheKey(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	first := e.created(e.body("2026-10-05", "09:00", "10:00", 1))

	// The second session overlaps the first, so it fails and must not hold keyB.
	body := e.body("2026-10-05", "09:30", "10:30", 1)
	s, r := e.callWithKey(http.MethodPost, "/entries", body, keyB)
	expectProblem(t, s, r, http.StatusConflict, "entry-overlap")
	if n := e.idempotencyRows(); n != 1 {
		t.Fatalf("idempotency rows = %d after the failure, want only the first create's", n)
	}

	// The user deletes the clash and retries with the same key and body.
	if s, r := e.call(http.MethodDelete, "/entries/"+first["id"].(string), ""); s != http.StatusNoContent {
		t.Fatalf("delete = %d %s", s, r)
	}
	if s, r := e.callWithKey(http.MethodPost, "/entries", body, keyB); s != http.StatusCreated {
		t.Fatalf("retry = %d %s", s, r)
	}
}

func TestIdempotency_AnExpiredKeyIsFreeAgain(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.actor = a
	if s, r := e.callWithKey(http.MethodPost, "/entries", e.body("2026-10-05", "09:00", "10:00", 1), keyA); s != http.StatusCreated {
		t.Fatalf("first = %d %s", s, r)
	}
	if _, err := e.conn.Exec(t.Context(), `UPDATE idempotency_keys SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day'`); err != nil {
		t.Fatal(err)
	}

	// A different payload is not a mismatch once the old record has expired.
	s, r := e.callWithKey(http.MethodPost, "/entries", e.body("2026-10-05", "13:00", "14:00", 1), keyA)
	if s != http.StatusCreated {
		t.Fatalf("reuse after expiry = %d %s", s, r)
	}
	if n := e.count(`SELECT count(*) FROM entries`); n != 2 {
		t.Errorf("entries = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM idempotency_keys WHERE expires_at > now()`); n != 1 {
		t.Errorf("live keys = %d, want the renewed one", n)
	}
}

func TestIdempotency_AClaimWhoseRequestDiedIsTakenOverAfterItsLease(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	e.actor = a
	body := e.body("2026-10-05", "09:00", "10:00", 1)
	hash := make([]byte, 32)
	if _, err := e.conn.Exec(t.Context(),
		`INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at)
		 VALUES ($1, $2, $3, 'in_progress', now() + interval '1 minute', now() + interval '1 day')`, a.ID, keyA, hash); err != nil {
		t.Fatal(err)
	}

	s, r := e.callWithKey(http.MethodPost, "/entries", body, keyA)
	if s != http.StatusConflict {
		t.Fatalf("while the claim is live = %d %s", s, r)
	}
	// A live claim with a different hash is a mismatch, not a wait.
	expectProblem(t, s, r, http.StatusConflict, "idempotency-key-reused")

	if _, err := e.conn.Exec(t.Context(), `UPDATE idempotency_keys SET locked_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if s, r := e.callWithKey(http.MethodPost, "/entries", body, keyA); s != http.StatusCreated {
		t.Fatalf("after the lease ran out = %d %s", s, r)
	}
}

func TestIdempotency_ClientsCreateIsReplayedToo(t *testing.T) {
	e := newEnv(t)
	e.actor = e.user("a@example.test")
	s1, r1 := e.callWithKey(http.MethodPost, "/clients", `{"name":"A.B."}`, keyA)
	s2, r2 := e.callWithKey(http.MethodPost, "/clients", `{"name":"A.B."}`, keyA)
	if s1 != http.StatusCreated || s2 != http.StatusCreated || r1 != r2 {
		t.Fatalf("responses = %d %s | %d %s", s1, r1, s2, r2)
	}
	if n := e.count(`SELECT count(*) FROM clients`); n != 1 {
		t.Errorf("clients = %d, want 1", n)
	}
}

func TestIdempotency_PurgeDeletesOnlyExpiredKeysInBatches(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	hash := make([]byte, 32)
	for i := range 5 {
		if _, err := e.conn.Exec(t.Context(),
			`INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, created_at, expires_at)
			 VALUES ($1, gen_random_uuid(), $2, 'in_progress', now(), now() - interval '2 days', now() - make_interval(hours => $3))`, a.ID, hash, 1+i); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := e.conn.Exec(t.Context(),
			`INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at)
			 VALUES ($1, gen_random_uuid(), $2, 'in_progress', now(), now() + interval '1 hour')`, a.ID, hash); err != nil {
			t.Fatal(err)
		}
	}

	store := db.NewIdempotencyStore(e.pool)
	if _, err := store.PurgeExpired(t.Context(), 0); err == nil {
		t.Error("a zero batch size was accepted")
	}
	deleted, err := store.PurgeExpired(t.Context(), 2) // Three batches: 2, 2, 1.
	if err != nil || deleted != 5 {
		t.Fatalf("PurgeExpired = %d, %v; want 5", deleted, err)
	}
	if n := e.idempotencyRows(); n != 2 {
		t.Errorf("remaining rows = %d, want the 2 live ones", n)
	}
	if deleted, err := store.PurgeExpired(t.Context(), 2); err != nil || deleted != 0 {
		t.Errorf("second PurgeExpired = %d, %v; want 0", deleted, err)
	}
}

func TestIdempotency_TheTableRefusesInconsistentRows(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	hash := make([]byte, 32)
	for name, query := range map[string]string{
		"completed without a response": `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at) VALUES ($1, gen_random_uuid(), $2, 'completed', now(), now() + interval '1 day')`,
		"in progress with a response":  `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, response_status, response_content_type, response_body, expires_at) VALUES ($1, gen_random_uuid(), $2, 'in_progress', now(), 201, 'application/json', '{}', now() + interval '1 day')`,
		"a stored error":               `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, response_status, response_content_type, response_body, expires_at) VALUES ($1, gen_random_uuid(), $2, 'completed', now(), 500, 'application/json', '{}', now() + interval '1 day')`,
		"a short request hash":         `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at) VALUES ($1, gen_random_uuid(), substr($2, 1, 5), 'in_progress', now(), now() + interval '1 day')`,
		"an unknown state":             `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at) VALUES ($1, gen_random_uuid(), $2, 'done', now(), now() + interval '1 day')`,
		"an expiry before creation":    `INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at) VALUES ($1, gen_random_uuid(), $2, 'in_progress', now(), now() - interval '1 day')`,
	} {
		if _, err := e.conn.Exec(t.Context(), query, a.ID, hash); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Deleting the user removes their keys.
	if _, err := e.conn.Exec(t.Context(),
		`INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at) VALUES ($1, gen_random_uuid(), $2, 'in_progress', now(), now() + interval '1 day')`, a.ID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := e.conn.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.idempotencyRows(); n != 0 {
		t.Errorf("rows after deleting the user = %d", n)
	}
}
