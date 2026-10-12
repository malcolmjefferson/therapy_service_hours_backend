package migrate_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

func TestIdentityAcceptanceFailureRollsBackProvisioningAndBinding(t *testing.T) {
	pool := seedPool(t)
	if _, err := pool.Exec(t.Context(), `INSERT INTO invitations(email,organization_id) SELECT 'invited@example.test',id FROM organizations WHERE name='Default Organization'; ALTER TABLE invitations ADD CONSTRAINT simulated_acceptance_failure CHECK (status='pending');`); err != nil {
		t.Fatal(err)
	}
	_, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "subject", Email: "invited@example.test"}, time.Now())
	if err == nil {
		t.Fatal("acceptance failure was ignored")
	}
	var users, pending int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM invitations WHERE status='pending' AND accepted_at IS NULL)`).Scan(&users, &pending); err != nil || users != 0 || pending != 1 {
		t.Fatalf("transaction did not roll back: %d %d %v", users, pending, err)
	}
}

func TestIdentityConcurrentEmailsCannotShareOneSubject(t *testing.T) {
	pool := seedPool(t)
	for _, email := range []string{"one@example.test", "two@example.test"} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO invitations(email,organization_id) SELECT $1,id FROM organizations WHERE name='Default Organization'`, email); err != nil {
			t.Fatal(err)
		}
	}
	store := db.NewIdentityStore(pool)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	for index, email := range []string{"one@example.test", "two@example.test"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failures[index] = store.Resolve(t.Context(), domain.Identity{Subject: "shared-sub", Email: email}, time.Now())
		}()
	}
	wg.Wait()
	if (failures[0] == nil) == (failures[1] == nil) {
		t.Fatalf("expected exactly one subject owner: %v", failures)
	}
	for _, err := range failures {
		if err != nil && !errors.Is(err, domain.ErrForbidden) {
			t.Fatal(err)
		}
	}
	var users, accepted int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM invitations WHERE status='accepted')`).Scan(&users, &accepted); err != nil || users != 1 || accepted != 1 {
		t.Fatalf("subject collision left partial state: %d %d %v", users, accepted, err)
	}
}
