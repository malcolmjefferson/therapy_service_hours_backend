package migrate_test

import (
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

// S3-07 tests: the dev-only approve-entries operation against real migrations. Users are fictional.

func TestSeedApproveEntries_ApprovesOnlyThePendingEntriesOfOneSyntheticUser(t *testing.T) {
	pool := seedPool(t)
	at := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "seeded-admin@example.test", IncludeDevUsers: true}); err != nil {
		t.Fatal(err)
	}
	insert := func(email, status string, minutes int) {
		t.Helper()
		_, err := pool.Exec(t.Context(),
			`INSERT INTO entries (user_id, session_date, duration_minutes, category_id, status, needs_time)
			 SELECT u.id, DATE '2026-10-01', $2, c.id, $3, true FROM users u, categories c WHERE u.email = $1 AND c.sort_order = 1`,
			email, minutes, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("test-invited@example.invalid", "pending", 60)
	insert("test-invited@example.invalid", "pending", 30)
	insert("test-invited@example.invalid", "rejected", 45)
	insert("test-supervisor@example.invalid", "pending", 15)
	insert("seeded-admin@example.test", "pending", 15)

	result, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "Test-Invited@example.invalid"})
	if err != nil || result.EntriesApproved != 2 {
		t.Fatalf("approved = %+v, %v", result, err)
	}
	count := func(email, status string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM entries e JOIN users u ON u.id = e.user_id WHERE u.email = $1 AND e.status = $2`, email, status).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, want := range []struct {
		email, status string
		n             int
	}{
		{"test-invited@example.invalid", "approved", 2},
		{"test-invited@example.invalid", "rejected", 1},
		{"test-invited@example.invalid", "pending", 0},
		{"test-supervisor@example.invalid", "pending", 1},
		{"seeded-admin@example.test", "pending", 1},
	} {
		if got := count(want.email, want.status); got != want.n {
			t.Errorf("%s %s = %d, want %d", want.email, want.status, got, want.n)
		}
	}

	// A repeat approves nothing more, and an unknown synthetic user is a no-op, not an error.
	if again, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "test-invited@example.invalid"}); err != nil || again.EntriesApproved != 0 {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if none, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "nobody@example.invalid"}); err != nil || none.EntriesApproved != 0 {
		t.Fatalf("unknown = %+v, %v", none, err)
	}
}

func TestSeedApproveEntries_RefusedInProdAndForRealAccounts(t *testing.T) {
	pool := seedPool(t)
	prod, _ := seed.New(db.NewSeedStore(pool), "prod", time.Now)
	if _, err := prod.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "test-invited@example.invalid"}); err == nil {
		t.Fatal("prod approved entries")
	}
	dev, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	if _, err := dev.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "seeded-admin@example.test"}); err == nil {
		t.Fatalf("real account was not refused: %v", err)
	}
}
