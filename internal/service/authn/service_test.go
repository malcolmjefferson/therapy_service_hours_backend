package authn_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
)

type storeFunc func(context.Context, domain.Identity, time.Time) (domain.Actor, error)

func (f storeFunc) Resolve(ctx context.Context, i domain.Identity, at time.Time) (domain.Actor, error) {
	return f(ctx, i, at)
}

func TestInvalidClaimsNeverReachStorage(t *testing.T) {
	base := map[string]string{"sub": "subject", "email": "user@example.test", "email_verified": "true", "token_use": "id"}
	for _, change := range []struct{ key, value string }{{"token_use", "access"}, {"sub", ""}, {"sub", strings.Repeat("s", 129)}, {"sub", " subject"}, {"email_verified", "false"}, {"email_verified", ""}, {"email", "Name <user@example.test>"}, {"email", " user@example.test"}, {"email", "bad\n@example.test"}, {"given_name", strings.Repeat("n", 201)}} {
		t.Run(change.key+change.value, func(t *testing.T) {
			claims := make(map[string]string)
			for k, v := range base {
				claims[k] = v
			}
			claims[change.key] = change.value
			svc, _ := authn.New(storeFunc(func(context.Context, domain.Identity, time.Time) (domain.Actor, error) {
				t.Fatal("invalid claims reached storage")
				return domain.Actor{}, nil
			}), time.Now)
			if _, err := svc.Resolve(t.Context(), claims); !errors.Is(err, authn.ErrUnauthorized) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestResolutionIgnoresGroupsAndUsesInjectedTime(t *testing.T) {
	at := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	want := domain.Actor{ID: "user", OrganizationID: "org", Active: true}
	svc, _ := authn.New(storeFunc(func(_ context.Context, i domain.Identity, got time.Time) (domain.Actor, error) {
		if i.Email != "user@example.test" || i.Subject != "subject" || !got.Equal(at) {
			t.Fatalf("identity/time = %+v %v", i, got)
		}
		return want, nil
	}), func() time.Time { return at })
	actor, err := svc.Resolve(t.Context(), map[string]string{"sub": "subject", "email": "USER@example.test", "email_verified": "true", "token_use": "id", "cognito:groups": "[admin,supervisor]"})
	if err != nil || actor != want {
		t.Fatalf("actor = %+v, %v", actor, err)
	}
	if _, err := authn.New(nil, time.Now); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := authn.New(storeFunc(nil), nil); err == nil {
		t.Fatal("nil clock accepted")
	}
}

func TestInactiveOrMissingActorIsRefused(t *testing.T) {
	for _, actor := range []domain.Actor{{}, {ID: "id", OrganizationID: "org"}, {ID: "id", Active: true}} {
		svc, _ := authn.New(storeFunc(func(context.Context, domain.Identity, time.Time) (domain.Actor, error) { return actor, nil }), time.Now)
		if _, err := svc.Resolve(t.Context(), map[string]string{"sub": "subject", "email": "user@example.test", "email_verified": "true", "token_use": "id"}); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("actor accepted: %+v", actor)
		}
	}
}
