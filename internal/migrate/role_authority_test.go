package migrate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

// Gateway signature/expiry validation precedes this boundary. Reusing the exact
// trusted claims models an unexpired token whose group membership is now stale.
func TestRoleRemovalAndDeactivationOverrideUnchangedTokenClaims(t *testing.T) {
	pool := seedPool(t)
	at := time.Now().UTC()
	seeder, err := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seeder.Run(t.Context(), seed.Request{AdminEmail: "role-owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	identityFixtureExec(t, pool, `UPDATE users SET is_supervisor=true WHERE email=$1::citext`, "role-owner@example.test")
	resolver, err := authn.New(db.NewIdentityStore(pool), func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	claims := roleClaims("role-owner@example.test")
	before, err := resolver.Resolve(t.Context(), claims)
	if err != nil || !authz.Admin(before) || !authz.Supervisor(before) {
		t.Fatal("current database privileges were not granted")
	}

	// Only the database changes. Neither time advancement, a fresh token nor
	// Cognito group synchronization is necessary for the next-request denial.
	identityFixtureExec(t, pool, `UPDATE users SET is_admin=false,is_supervisor=false WHERE id=$1::uuid`, before.ID)
	after, err := resolver.Resolve(t.Context(), claims)
	if err != nil || after.ID != before.ID || authz.Admin(after) || authz.Supervisor(after) {
		t.Fatal("stale token retained removed privileges")
	}
	if !authz.Self(after, authz.Resource{UserID: after.ID, OrganizationID: after.OrganizationID}) {
		t.Fatal("role removal unexpectedly removed ordinary self access")
	}
	identityFixtureExec(t, pool, `UPDATE users SET status='deactivated',deactivated_at=$2 WHERE id=$1::uuid`, before.ID, at)
	if _, err := resolver.Resolve(t.Context(), claims); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("deactivated account accepted unchanged token claims")
	}
}

func TestTokenGroupsCannotElevateAnOrdinaryDatabaseUser(t *testing.T) {
	pool := seedPool(t)
	identityFixtureExec(t, pool, `INSERT INTO invitations (email,organization_id) SELECT $1,id FROM organizations WHERE name='Default Organization'`, "ordinary@example.test")
	resolver, err := authn.New(db.NewIdentityStore(pool), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	claims := roleClaims("ordinary@example.test")
	for _, groups := range []string{"[admin,supervisor]", "admin", "[\"admin\",\"supervisor\"]", ""} {
		claims["cognito:groups"] = groups
		actor, err := resolver.Resolve(t.Context(), claims)
		if err != nil || authz.Admin(actor) || authz.Supervisor(actor) {
			t.Fatal("token group claim elevated an ordinary database user")
		}
	}
}

func roleClaims(email string) map[string]string {
	return map[string]string{
		"sub": "fictional-role-subject", "email": email,
		"email_verified": "true", "token_use": "id",
		"cognito:groups": "[admin,supervisor]",
	}
}
