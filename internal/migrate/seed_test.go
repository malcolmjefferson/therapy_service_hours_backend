package migrate_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func seedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	name := server.NewDatabase(t)
	pool, err := db.Open(t.Context(), db.Settings{Host: server.Host, Port: server.Port, Database: name, User: server.User, Mode: db.AuthPassword, Password: server.Password}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = conn.Close() })
	if result := migrate.Run(t.Context(), conn, migrations.FS, migrate.Options{}); result.Status != migrate.StatusOK {
		t.Fatalf("migrate failed: %+v", result)
	}
	return pool
}

func TestIdentitySeedIsIdempotentAndPreservesBinding(t *testing.T) {
	pool := seedPool(t)
	at := time.Now().UTC()
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	request := seed.Request{AdminEmail: "ADMIN@example.test", IncludeDevUsers: true}
	first, err := svc.Run(t.Context(), request)
	if err != nil || first.AccountsCreated != 3 || first.InvitationsCreated != 3 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	var before, after string
	snapshot := `SELECT jsonb_build_object('users', (SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u), 'invitations', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i))::text`
	if err := pool.QueryRow(t.Context(), snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Run(t.Context(), request)
	if err != nil || !reflect.DeepEqual(second, seed.Result{}) {
		t.Fatalf("repeat = %+v, %v", second, err)
	}
	if err := pool.QueryRow(t.Context(), snapshot).Scan(&after); err != nil || after != before {
		t.Fatal("repeat changed database state")
	}
	var uninvited int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE email = 'test-uninvited@example.invalid') + (SELECT count(*) FROM invitations WHERE email = 'test-uninvited@example.invalid')`).Scan(&uninvited); err != nil || uninvited != 0 {
		t.Fatal("uninvited fixture was accidentally admitted")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET cognito_sub = 'bound-admin' WHERE email = 'admin@example.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE invitations SET status = 'accepted', accepted_at = now() WHERE email = 'admin@example.test'`); err != nil {
		t.Fatal(err)
	}
	third, err := svc.Run(t.Context(), request)
	if err != nil || third.InvitationsCreated != 0 {
		t.Fatal("bound identity was re-invited")
	}
}

func TestExpiredSeedInvitationIsRevokedAndRenewedOnce(t *testing.T) {
	pool := seedPool(t)
	at := time.Now().UTC()
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	request := seed.Request{AdminEmail: "admin@example.test"}
	if _, err := svc.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	at = at.Add(14 * 24 * time.Hour) // Exact expiry is a refusal; renewal keeps the old history.
	result, err := svc.Run(t.Context(), request)
	if err != nil || result.InvitationsRenewed != 1 || result.InvitationsCreated != 1 {
		t.Fatalf("renew = %+v, %v", result, err)
	}
	var revoked, pending int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status = 'revoked'), count(*) FILTER (WHERE status = 'pending') FROM invitations`).Scan(&revoked, &pending); err != nil || revoked != 1 || pending != 1 {
		t.Fatal("renewal lost history or created duplicates")
	}
	result, err = svc.Run(t.Context(), request)
	if err != nil || result.InvitationsCreated != 0 || result.InvitationsRenewed != 0 {
		t.Fatal("repeat extended invitation lifetime")
	}
}

func TestSeedConflictsRollBackEarlierAccountsAndNeverReactivateOrPromote(t *testing.T) {
	for _, state := range []string{"deactivated", "wrong-role", "wrong-invitation"} {
		t.Run(state, func(t *testing.T) {
			pool := seedPool(t)
			if _, err := pool.Exec(t.Context(), `INSERT INTO users (email, organization_id) SELECT 'test-supervisor@example.invalid', id FROM organizations WHERE name = 'Default Organization'`); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "deactivated":
				_, err := pool.Exec(t.Context(), `UPDATE users SET is_supervisor = true, status = 'deactivated', deactivated_at = now()`)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-invitation":
				_, err := pool.Exec(t.Context(), `UPDATE users SET is_supervisor = true`)
				if err != nil {
					t.Fatal(err)
				}
				_, err = pool.Exec(t.Context(), `INSERT INTO invitations (email, organization_id, roles) SELECT 'test-supervisor@example.invalid', id, ARRAY['admin'] FROM organizations WHERE name = 'Default Organization'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
			_, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test", IncludeDevUsers: true})
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("conflict = %v", err)
			}
			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 1 {
				t.Fatal("earlier account writes were not rolled back")
			}
		})
	}
}

func TestConcurrentSeedsCreateOneIdentityAndInvitation(t *testing.T) {
	pool := seedPool(t)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	var wg sync.WaitGroup
	results := make([]seed.Result, 2)
	errorsFound := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errorsFound[i] = svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test"})
		}()
	}
	wg.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || results[0].AccountsCreated+results[1].AccountsCreated != 1 || results[0].InvitationsCreated+results[1].InvitationsCreated != 1 {
		t.Fatalf("concurrent results = %+v, errors = %v", results, errorsFound)
	}
}

func TestSeedEmailWithSQLMetacharactersIsBoundAsData(t *testing.T) {
	pool := seedPool(t)
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", time.Now)
	email := "admin'+or+1=1--@example.test"
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: email}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE email = $1::citext`, email).Scan(&count); err != nil || count != 1 {
		t.Fatal("email was not treated as data")
	}
}

func TestSeedRunsWithTableDMLOnly(t *testing.T) {
	pool := seedPool(t)
	_, err := pool.Exec(t.Context(), `DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'seed_test_writer') THEN CREATE ROLE seed_test_writer LOGIN PASSWORD 'local-test-password'; END IF; END $$;
	GRANT USAGE ON SCHEMA public TO seed_test_writer;
	GRANT SELECT, INSERT, UPDATE, DELETE ON organizations, users, invitations, goals TO seed_test_writer;`)
	if err != nil {
		t.Fatal(err)
	}
	settings := db.Settings{Host: server.Host, Port: server.Port, Database: pool.Config().ConnConfig.Database, User: "seed_test_writer", Mode: db.AuthPassword, Password: "local-test-password"}
	dmlPool, err := db.Open(t.Context(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dmlPool.Close)
	svc, _ := seed.New(db.NewSeedStore(dmlPool), "dev", time.Now)
	result, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test", IncludeDevUsers: true})
	if err != nil || result.AccountsCreated != 3 {
		t.Fatalf("DML-only seed = %+v, %v", result, err)
	}
	if _, err := dmlPool.Exec(t.Context(), `CREATE TABLE seed_should_not_create_ddl (id integer)`); err == nil {
		t.Fatal("seed role has unexpected DDL rights")
	}
}
