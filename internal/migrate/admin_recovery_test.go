package migrate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func TestAdditionalAdminRecoveryPreservesOriginalAccount(t *testing.T) {
	for _, environment := range []string{"dev", "prod"} {
		t.Run(environment, func(t *testing.T) {
			pool := seedPool(t)
			at := time.Now().UTC()
			svc, err := seed.New(db.NewSeedStore(pool), environment, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			first := seed.Request{AdminEmail: "original-admin@example.test", AllowProd: environment == "prod"}
			if _, err := svc.Run(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			identity := domain.Identity{Subject: "original-admin-subject", Email: first.AdminEmail}
			if _, err := db.NewIdentityStore(pool).Resolve(t.Context(), identity, at); err != nil {
				t.Fatal(err)
			}
			before := recoveryAccountSnapshot(t, pool, first.AdminEmail)
			additional := seed.Request{AdminEmail: "recovery-admin@example.test", AllowProd: environment == "prod"}
			assertAdditionalAdminSeed(t, pool, svc, additional, at)
			if after := recoveryAccountSnapshot(t, pool, first.AdminEmail); after != before {
				t.Fatal("additional admin seed changed the original bound account or invitation history")
			}
		})
	}
}

func assertAdditionalAdminSeed(t *testing.T, pool *pgxpool.Pool, svc *seed.Service, request seed.Request, at time.Time) {
	t.Helper()
	result, err := svc.Run(t.Context(), request)
	if err != nil || result.AccountsCreated != 1 || result.InvitationsCreated != 1 || result.InvitationsRenewed != 0 {
		t.Fatal("additional admin was not created with exactly one new invitation")
	}
	before := recoveryAccountSnapshot(t, pool, request.AdminEmail)
	result, err = svc.Run(t.Context(), request)
	if err != nil || result != (seed.Result{}) || recoveryAccountSnapshot(t, pool, request.AdminEmail) != before {
		t.Fatal("repeating additional admin seed changed its identity or invitation")
	}
	actor, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "recovery-admin-subject", Email: request.AdminEmail}, at)
	if err != nil || !actor.Admin || !actor.Active || actor.Supervisor {
		t.Fatal("additional admin could not bind its own identity with the seeded roles")
	}
}

func recoveryAccountSnapshot(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(t.Context(), `SELECT jsonb_build_object('user',to_jsonb(u),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM invitations i WHERE i.email=u.email))::text FROM users u WHERE u.email=$1::citext`, email).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestProdRecoveryWithoutExplicitFlagLeavesDatabaseUnchanged(t *testing.T) {
	pool := seedPool(t)
	svc, err := seed.New(db.NewSeedStore(pool), "prod", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "recovery-admin@example.test"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("production recovery accepted a missing explicit flag")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM invitations)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("refused production recovery changed database state")
	}
}
