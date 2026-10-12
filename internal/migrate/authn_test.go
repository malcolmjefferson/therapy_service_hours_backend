package migrate_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func TestIdentityFirstSignInConcurrentAndAcceptanceStampedOnce(t *testing.T) {
	pool := seedPool(t)
	_, err := pool.Exec(t.Context(), `INSERT INTO invitations (email,organization_id,roles) SELECT $1,id,ARRAY['supervisor'] FROM organizations WHERE name='Default Organization'`, "invited@example.test")
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewIdentityStore(pool)
	identity := domain.Identity{Subject: "new-sub", Email: "invited@example.test", FirstName: "Pseudonym", LastName: "A"}
	at := time.Now().UTC()
	var wg sync.WaitGroup
	actors := make([]domain.Actor, 8)
	failures := make([]error, 8)
	for index := range actors {
		wg.Add(1)
		go func() { defer wg.Done(); actors[index], failures[index] = store.Resolve(t.Context(), identity, at) }()
	}
	wg.Wait()
	for i, a := range actors {
		if failures[i] != nil || a.ID != actors[0].ID || !a.Supervisor || a.Admin {
			t.Fatalf("concurrent identity %d = %+v, %v", i, a, failures[i])
		}
	}
	var users, accepted int
	var stamp time.Time
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM invitations WHERE status='accepted'),accepted_at FROM invitations WHERE email=$1::citext`, identity.Email).Scan(&users, &accepted, &stamp); err != nil || users != 1 || accepted != 1 || stamp.Sub(at).Abs() > time.Microsecond {
		t.Fatalf("acceptance counts/time %d %d %v %v", users, accepted, stamp, err)
	}
	assertReturningActor(t, pool, identity, at, stamp)
}

func assertReturningActor(t *testing.T, pool *pgxpool.Pool, identity domain.Identity, at, stamp time.Time) {
	t.Helper()
	store := db.NewIdentityStore(pool)
	identity.FirstName = "Changed"
	identity.LastName = "Token"
	if _, err := pool.Exec(t.Context(), `UPDATE users SET is_supervisor=false,is_admin=true WHERE email=$1::citext`, identity.Email); err != nil {
		t.Fatal(err)
	}
	a, err := store.Resolve(t.Context(), identity, at.Add(time.Hour))
	if err != nil || a.FirstName != "Pseudonym" || a.Supervisor || !a.Admin {
		t.Fatalf("database roles/profile not authoritative: %+v %v", a, err)
	}
	var after time.Time
	if err := pool.QueryRow(t.Context(), `SELECT accepted_at FROM invitations WHERE email=$1::citext`, identity.Email).Scan(&after); err != nil || !after.Equal(stamp) {
		t.Fatal("acceptance timestamp changed")
	}
	if pool.Config().MaxConns != 2 {
		t.Fatal("connection budget changed")
	}
	identityFixtureExec(t, pool, `UPDATE users SET is_admin=false WHERE email=$1::citext`, identity.Email)
	demoted, err := store.Resolve(t.Context(), identity, at.Add(2*time.Hour))
	if err != nil || demoted.Admin || demoted.Supervisor {
		t.Fatal("role revocation did not take effect on next request")
	}
}

func TestIdentitySeededAdminBindsWithoutChangingRolesOrID(t *testing.T) {
	pool := seedPool(t)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test"}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM users WHERE email='admin@example.test'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	a, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "admin-sub", Email: "ADMIN@example.test", FirstName: "Token Name"}, time.Now())
	if err != nil || a.ID != before || !a.Admin || a.FirstName == "Token Name" {
		t.Fatalf("seeded admin = %+v, %v", a, err)
	}
}

func TestIdentityRefusesUninvitedExpiredRevokedAndDeactivated(t *testing.T) {
	for _, state := range []string{"uninvited", "expired", "revoked", "deactivated", "bound-other-sub", "wrong-organization"} {
		t.Run(state, func(t *testing.T) {
			pool := seedPool(t)
			email := "denied@example.test"
			at := time.Now().UTC()
			if state != "uninvited" {
				if _, err := pool.Exec(t.Context(), `INSERT INTO invitations (email,organization_id,created_at,expires_at) SELECT $1,id,$2::timestamptz-interval '1 day',$2::timestamptz+interval '1 day' FROM organizations WHERE name='Default Organization'`, email, at); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "expired":
				identityFixtureExec(t, pool, `UPDATE invitations SET expires_at=$1`, at)
			case "revoked":
				identityFixtureExec(t, pool, `UPDATE invitations SET status='revoked'`)
			case "deactivated":
				identityFixtureExec(t, pool, `INSERT INTO users (email,organization_id,status,deactivated_at) SELECT $1,id,'deactivated',now() FROM organizations WHERE name='Default Organization'`, email)
			case "bound-other-sub":
				identityFixtureExec(t, pool, `INSERT INTO users (email,organization_id,cognito_sub) SELECT $1,id,'other-sub' FROM organizations WHERE name='Default Organization'`, email)
			case "wrong-organization":
				identityFixtureExec(t, pool, `INSERT INTO organizations(name) VALUES ('Other Organization');`)
				identityFixtureExec(t, pool, `INSERT INTO users(email,organization_id) SELECT $1,id FROM organizations WHERE name='Other Organization'`, email)
			}
			_, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "denied-sub", Email: email}, at)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("state %s error=%v", state, err)
			}
			var bound, accepted int
			if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE cognito_sub='denied-sub'),(SELECT count(*) FROM invitations WHERE status='accepted')`).Scan(&bound, &accepted); err != nil || bound != 0 || accepted != 0 {
				t.Fatal("denial changed identity state")
			}
		})
	}
}

func TestIdentityRejectsEmailChangesAndDeactivationWithBoundSubject(t *testing.T) {
	pool := seedPool(t)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test"}); err != nil {
		t.Fatal(err)
	}
	store := db.NewIdentityStore(pool)
	i := domain.Identity{Subject: "subject", Email: "admin@example.test"}
	if _, err := store.Resolve(t.Context(), i, time.Now()); err != nil {
		t.Fatal(err)
	}
	i.Email = "changed@example.test"
	if _, err := store.Resolve(t.Context(), i, time.Now()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("changed token email accepted")
	}
	i.Email = "admin@example.test"
	_, err := pool.Exec(t.Context(), `UPDATE users SET status='deactivated',deactivated_at=now()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(t.Context(), i, time.Now()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("deactivated identity accepted")
	}
}

func TestIdentityCompetingSubjectsCannotRebindAnEmail(t *testing.T) {
	pool := seedPool(t)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test"}); err != nil {
		t.Fatal(err)
	}
	store := db.NewIdentityStore(pool)
	var wg sync.WaitGroup
	failures := make([]error, 2)
	for index, sub := range []string{"subject-a", "subject-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failures[index] = store.Resolve(t.Context(), domain.Identity{Subject: sub, Email: "admin@example.test"}, time.Now())
		}()
	}
	wg.Wait()
	if (failures[0] == nil) == (failures[1] == nil) {
		t.Fatalf("expected exactly one binding: %v", failures)
	}
	for _, err := range failures {
		if err != nil && !errors.Is(err, domain.ErrForbidden) {
			t.Fatal(err)
		}
	}
}

func identityFixtureExec(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityEmailAndSubjectSQLMetacharactersAreData(t *testing.T) {
	pool := seedPool(t)
	email := "user'+or+1=1--@example.test"
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: email}); err != nil {
		t.Fatal(err)
	}
	a, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "sub' OR '1'='1", Email: email}, time.Now())
	if err != nil || a.Email != email {
		t.Fatalf("injection data failed: %+v %v", a, err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 1 {
		t.Fatal("query scope changed")
	}
}
