package migrate_test

import (
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func TestOperatorBindingVerificationIsReadOnlyAcrossIdentityStates(t *testing.T) {
	pool := seedPool(t)
	at := time.Now().UTC()
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "binding-owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	request := seed.BindingRequest{Email: "binding-owner@example.test", Subject: "fictional-role-subject"}
	snapshot := `SELECT jsonb_build_object('users', (SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u), 'invitations', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i))::text`
	check := func(request seed.BindingRequest, want seed.BindingResult) {
		t.Helper()
		var before, after string
		if err := pool.QueryRow(t.Context(), snapshot).Scan(&before); err != nil {
			t.Fatal(err)
		}
		got, err := svc.VerifyBinding(t.Context(), request)
		if err != nil || got != want {
			t.Fatalf("unexpected verification counts: %+v, %v", got, err)
		}
		if err := pool.QueryRow(t.Context(), snapshot).Scan(&after); err != nil || after != before {
			t.Fatal("verification changed users or invitations")
		}
	}
	check(request, seed.BindingResult{Users: 1})
	resolver, _ := authn.New(db.NewIdentityStore(pool), func() time.Time { return at })
	actor, err := resolver.Resolve(t.Context(), roleClaims(request.Email))
	if err != nil {
		t.Fatal(err)
	}
	bound := seed.BindingResult{Users: 1, SubjectUsers: 1, MatchingActiveAdmin: 1, AcceptedInvitations: 1}
	check(request, bound)
	check(request, bound)
	check(seed.BindingRequest{Email: request.Email, Subject: "different-subject"}, seed.BindingResult{Users: 1})
	check(seed.BindingRequest{Email: "missing@example.test", Subject: request.Subject}, seed.BindingResult{SubjectUsers: 1})
	identityFixtureExec(t, pool, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, actor.ID)
	bound.MatchingActiveAdmin = 0
	check(request, bound)
	identityFixtureExec(t, pool, `UPDATE users SET is_admin=true,status='deactivated',deactivated_at=$2 WHERE id=$1::uuid`, actor.ID, at)
	check(request, bound)
}
