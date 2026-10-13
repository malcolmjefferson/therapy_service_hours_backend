package migrate_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// S2-07 store tests: the real ClientStore against PostgreSQL with the real migrations (GO-72).
// Users and clients are fictional pseudonyms (CLAUDE.md §0.7).

const unknownClientID = "00000000-0000-4000-8000-00000000ffff"

func createClient(t *testing.T, store *db.ClientStore, user, name string) domain.Client {
	t.Helper()
	c, err := store.Create(t.Context(), user, name)
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return c
}

func useClient(t *testing.T, pool *pgxpool.Pool, user, clientID, date, start, end string, minutes int) string {
	t.Helper()
	entryID := mustInsertEntry(t, pool, timed(user, date, start, end, minutes))
	if _, err := pool.Exec(t.Context(), `INSERT INTO entry_clients (entry_id, client_id, user_id) VALUES ($1, $2, $3)`, entryID, clientID, user); err != nil {
		t.Fatalf("attach client: %v", err)
	}
	return entryID
}

func names(items []domain.Client) []string {
	out := make([]string, 0, len(items))
	for _, c := range items {
		out = append(out, c.Name)
	}
	return out
}

func TestClientStore_ListIsOrderedPagedAndScopedToTheOwner(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice := insertUser(t, pool, "alice@example.test")
	for _, n := range []string{"beta", "Alpha", "Delta", "charlie", "Echo"} {
		createClient(t, store, alice, n)
	}

	items, total, err := store.List(t.Context(), alice, 3, 0)
	if err != nil || total != 5 {
		t.Fatalf("page 1: total = %d, err = %v", total, err)
	}
	if got := strings.Join(names(items), ","); got != "Alpha,beta,charlie" {
		t.Errorf("page 1 = %s, want case-insensitive name order", got)
	}
	items, total, err = store.List(t.Context(), alice, 3, 3)
	if err != nil || total != 5 || strings.Join(names(items), ",") != "Delta,Echo" {
		t.Errorf("page 2 = %v, total = %d, err = %v", names(items), total, err)
	}
	items, total, err = store.List(t.Context(), alice, 3, 300)
	if err != nil || total != 5 || len(items) != 0 || items == nil {
		t.Errorf("past the end: items = %v, total = %d, err = %v (want an empty non-nil page)", items, total, err)
	}
}

func TestClientStore_ListShowsOnlyTheOwnersClients(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice, bob := insertUser(t, pool, "alice@example.test"), insertUser(t, pool, "bob@example.test")
	createClient(t, store, alice, "Alice's Group")
	createClient(t, store, bob, "Bob's Group")
	items, total, err := store.List(t.Context(), bob, 50, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].Name != "Bob's Group" || items[0].ID == "" {
		t.Errorf("bob sees %v (total %d), err = %v", items, total, err)
	}
}

func TestClientStore_NamesAreUniquePerUserIgnoringCase(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice, bob := insertUser(t, pool, "alice@example.test"), insertUser(t, pool, "bob@example.test")
	original := createClient(t, store, alice, "A.B.")

	_, err := store.Create(t.Context(), alice, "a.b.")
	var taken *domain.ClientNameTakenError
	if !errors.As(err, &taken) || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate = %v, want ClientNameTakenError", err)
	}
	if taken.Existing != original {
		t.Errorf("existing = %+v, want %+v", taken.Existing, original)
	}
	if got := count(t, pool, `SELECT count(*) FROM clients WHERE user_id = $1`, alice); got != 1 {
		t.Errorf("alice has %d clients, want 1", got)
	}
	// The same name is free for another user.
	createClient(t, store, bob, "a.b.")
}

func TestClientStore_ConcurrentCreatesOfOneNameYieldOneClient(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice := insertUser(t, pool, "alice@example.test")
	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	existing := make([]domain.Client, workers)
	created := make([]domain.Client, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "Group Race"
			if i%2 == 1 {
				name = "GROUP RACE"
			}
			created[i], results[i] = store.Create(t.Context(), alice, name)
			var taken *domain.ClientNameTakenError
			if errors.As(results[i], &taken) {
				existing[i] = taken.Existing
			}
		}()
	}
	wg.Wait()
	var winner domain.Client
	wins := 0
	for i, err := range results {
		if err == nil {
			wins++
			winner = created[i]
		}
	}
	if wins != 1 {
		t.Fatalf("%d creates succeeded, want exactly 1: %v", wins, results)
	}
	for i, err := range results {
		if err == nil {
			continue
		}
		var taken *domain.ClientNameTakenError
		if !errors.As(err, &taken) {
			t.Fatalf("loser %d got %v, want ClientNameTakenError", i, err)
		}
		if existing[i] != winner {
			t.Errorf("loser %d was told %+v, want the winner %+v", i, existing[i], winner)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM clients WHERE user_id = $1`, alice); got != 1 {
		t.Errorf("%d clients saved, want 1", got)
	}
}

func TestClientStore_Rename(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice, bob := insertUser(t, pool, "alice@example.test"), insertUser(t, pool, "bob@example.test")
	first := createClient(t, store, alice, "First")
	second := createClient(t, store, alice, "Second")
	bobs := createClient(t, store, bob, "Bob's")

	t.Run("renames", func(t *testing.T) {
		got, err := store.Rename(t.Context(), alice, first.ID, "Primary")
		if err != nil || got.ID != first.ID || got.Name != "Primary" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("changing only the case of its own name is allowed", func(t *testing.T) {
		got, err := store.Rename(t.Context(), alice, first.ID, "PRIMARY")
		if err != nil || got.Name != "PRIMARY" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("another of the user's names is a conflict naming that client", func(t *testing.T) {
		_, err := store.Rename(t.Context(), alice, first.ID, "second")
		var taken *domain.ClientNameTakenError
		if !errors.As(err, &taken) || taken.Existing != second {
			t.Fatalf("err = %v, existing = %+v, want %+v", err, taken, second)
		}
		if got := count(t, pool, `SELECT count(*) FROM clients WHERE id = $1 AND name = 'PRIMARY'`, first.ID); got != 1 {
			t.Error("a rejected rename changed the client")
		}
	})
	t.Run("a name another user has is free", func(t *testing.T) {
		if _, err := store.Rename(t.Context(), alice, first.ID, "Bob's"); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("another user's client is not found and unchanged", func(t *testing.T) {
		_, err := store.Rename(t.Context(), alice, bobs.ID, "Hijacked")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := count(t, pool, `SELECT count(*) FROM clients WHERE id = $1 AND name = 'Bob''s'`, bobs.ID); got != 1 {
			t.Error("another user's client was renamed")
		}
	})
	t.Run("an unknown or malformed id is not found", func(t *testing.T) {
		for _, id := range []string{unknownClientID, "not-a-uuid", ""} {
			if _, err := store.Rename(t.Context(), alice, id, "X"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("id %q: err = %v, want ErrNotFound", id, err)
			}
		}
	})
}

func TestClientStore_Delete(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice := insertUser(t, pool, "alice@example.test")

	t.Run("an unused client is deleted without confirmation", func(t *testing.T) {
		c := createClient(t, store, alice, "Unused")
		if err := store.Delete(t.Context(), alice, c.ID, false); err != nil {
			t.Fatal(err)
		}
		if got := count(t, pool, `SELECT count(*) FROM clients WHERE id = $1`, c.ID); got != 0 {
			t.Error("client still exists")
		}
	})
	t.Run("a client that entries use needs confirmation", func(t *testing.T) {
		c := createClient(t, store, alice, "In use")
		useClient(t, pool, alice, c.ID, "2026-10-12", "09:00", "10:00", 60)
		useClient(t, pool, alice, c.ID, "2026-10-13", "09:00", "10:00", 60)
		err := store.Delete(t.Context(), alice, c.ID, false)
		var inUse *domain.ClientInUseError
		if !errors.As(err, &inUse) || inUse.EntryCount != 2 || !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("err = %v, want ClientInUseError with 2 entries", err)
		}
		if got := count(t, pool, `SELECT count(*) FROM clients WHERE id = $1`, c.ID); got != 1 {
			t.Error("client was deleted without confirmation")
		}
		if got := count(t, pool, `SELECT count(*) FROM entry_clients WHERE client_id = $1`, c.ID); got != 2 {
			t.Errorf("%d entries still use the client, want 2", got)
		}
	})
	t.Run("with confirmation the client is detached and deleted but the entries stay", func(t *testing.T) {
		c := createClient(t, store, alice, "Detach me")
		entryID := useClient(t, pool, alice, c.ID, "2026-10-14", "09:00", "10:00", 60)
		if err := store.Delete(t.Context(), alice, c.ID, true); err != nil {
			t.Fatal(err)
		}
		if got := count(t, pool, `SELECT count(*) FROM clients WHERE id = $1`, c.ID); got != 0 {
			t.Error("client still exists")
		}
		if got := count(t, pool, `SELECT count(*) FROM entry_clients WHERE client_id = $1`, c.ID); got != 0 {
			t.Error("client is still attached to an entry")
		}
		if got := count(t, pool, `SELECT count(*) FROM entries WHERE id = $1`, entryID); got != 1 {
			t.Error("detaching a client deleted the entry")
		}
	})
}

func TestClientStore_DeleteIsScopedToTheOwner(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice, bob := insertUser(t, pool, "alice@example.test"), insertUser(t, pool, "bob@example.test")

	t.Run("another user's client is not found and untouched", func(t *testing.T) {
		c := createClient(t, store, bob, "Bob's group")
		useClient(t, pool, bob, c.ID, "2026-10-12", "09:00", "10:00", 60)
		for _, detach := range []bool{false, true} {
			if err := store.Delete(t.Context(), alice, c.ID, detach); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("detach=%v: err = %v, want ErrNotFound", detach, err)
			}
		}
		if got := count(t, pool, `SELECT count(*) FROM clients c JOIN entry_clients ec ON ec.client_id = c.id WHERE c.id = $1`, c.ID); got != 1 {
			t.Error("another user's client or its attachment was removed")
		}
	})
	t.Run("an unknown or malformed id is not found", func(t *testing.T) {
		for _, id := range []string{unknownClientID, "not-a-uuid", ""} {
			if err := store.Delete(t.Context(), alice, id, true); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("id %q: err = %v, want ErrNotFound", id, err)
			}
		}
	})
	t.Run("deleting twice is not found the second time", func(t *testing.T) {
		c := createClient(t, store, alice, "Twice")
		if err := store.Delete(t.Context(), alice, c.ID, false); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(t.Context(), alice, c.ID, false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("second delete: err = %v, want ErrNotFound", err)
		}
	})
}

func TestClientStore_InjectionPayloadsAreStoredAsPlainText(t *testing.T) {
	pool := seedPool(t)
	store := db.NewClientStore(pool)
	alice := insertUser(t, pool, "alice@example.test")
	payloads := []string{`' OR 1=1 --`, `'; DROP TABLE clients; --`, `A\B`, `100%`, `under_score`, `"quoted"`, "tab\tname", `$1`, `Ünïcödé 名前`}
	for _, p := range payloads {
		created := createClient(t, store, alice, p)
		if created.Name != p {
			t.Errorf("stored %q as %q", p, created.Name)
		}
	}
	items, total, err := store.List(t.Context(), alice, 50, 0)
	if err != nil || total != int64(len(payloads)) {
		t.Fatalf("total = %d, err = %v", total, err)
	}
	stored := map[string]bool{}
	for _, c := range items {
		stored[c.Name] = true
	}
	for _, p := range payloads {
		if !stored[p] {
			t.Errorf("payload %q was not stored literally (got %v)", p, names(items))
		}
	}
	// Lookups by name match literally: '%' does not match every client, and a quote does not widen the match.
	if _, err := store.Create(t.Context(), alice, `' or 1=1 --`); err == nil || !errors.Is(err, domain.ErrConflict) {
		t.Errorf("case-insensitive duplicate of a payload = %v, want a conflict", err)
	}
	if _, err := store.Create(t.Context(), alice, `%`); err != nil {
		t.Errorf("'%%' must not match other names, got %v", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM clients`); got != len(payloads)+1 {
		t.Errorf("%d clients, want %d: %s", got, len(payloads)+1, fmt.Sprint(names(items)))
	}
}
