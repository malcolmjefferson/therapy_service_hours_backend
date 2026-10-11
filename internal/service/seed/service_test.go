package seed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

type store struct {
	calls int
	plan  seed.Plan
	err   error
}

func (s *store) Apply(_ context.Context, plan seed.Plan) (seed.Result, error) {
	s.calls++
	s.plan = plan
	return seed.Result{AccountsCreated: int64(len(plan.Accounts))}, s.err
}

func TestEnvironmentAndInputGuardsRunBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name, env, email string
		allow, dev       bool
	}{
		{"prod requires explicit flag", "prod", "admin@example.test", false, false},
		{"prod forbids dev fixtures even with flag", "prod", "admin@example.test", true, true},
		{"missing email", "dev", "", false, false},
		{"display name", "dev", "Name <admin@example.test>", false, false},
		{"test identity cannot become admin", "dev", "test-invited@example.invalid", false, false},
		{"invalid email", "dev", "invalid", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &store{}
			svc, err := seed.New(db, tc.env, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: tc.email, AllowProd: tc.allow, IncludeDevUsers: tc.dev}); err == nil {
				t.Fatal("unsafe seed accepted")
			}
			if db.calls != 0 {
				t.Fatal("invalid request reached storage")
			}
		})
	}
}

func TestDevPlanExcludesUninvitedAndProdPlanExcludesAllTestUsers(t *testing.T) {
	for _, env := range []string{"dev", "prod"} {
		t.Run(env, func(t *testing.T) {
			db := &store{}
			at := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
			svc, _ := seed.New(db, env, func() time.Time { return at })
			_, err := svc.Run(t.Context(), seed.Request{AdminEmail: " Admin@Example.Test ", IncludeDevUsers: env == "dev", AllowProd: env == "prod"})
			if err != nil {
				t.Fatal(err)
			}
			if db.plan.At != at || db.plan.Accounts[0].Email != "admin@example.test" || !db.plan.Accounts[0].Admin {
				t.Fatal("admin plan is invalid")
			}
			want := 1
			if env == "dev" {
				want = 3
			}
			if len(db.plan.Accounts) != want {
				t.Fatal("unexpected fixture count")
			}
			for _, account := range db.plan.Accounts {
				if account.Email == "test-uninvited@example.invalid" {
					t.Fatal("uninvited user must have no database identity")
				}
			}
		})
	}
}

func TestStoreConflictIsPreservedWithoutPartialCounts(t *testing.T) {
	svc, _ := seed.New(&store{err: domain.ErrConflict}, "dev", time.Now)
	result, err := svc.Run(t.Context(), seed.Request{AdminEmail: "admin@example.test"})
	if !errors.Is(err, domain.ErrConflict) || result.AccountsCreated != 0 {
		t.Fatal("lost safe conflict or returned partial counts")
	}
}
