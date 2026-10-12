package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

type links struct {
	called  bool
	allowed bool
	err     error
}

func (l *links) SupervisorOf(context.Context, string, string, string) (bool, error) {
	l.called = true
	return l.allowed, l.err
}
func (l *links) Peers(context.Context, string, string, string) (bool, error) {
	l.called = true
	return l.allowed, l.err
}

func TestRoleAndSelfPoliciesDenyByDefault(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		actor                   domain.Actor
		admin, supervisor, self bool
	}{
		{"absent", domain.Actor{}, false, false, false},
		{"inactive", domain.Actor{ID: "a", OrganizationID: "org", Admin: true, Supervisor: true}, false, false, false},
		{"ordinary", domain.Actor{ID: "a", OrganizationID: "org", Active: true}, false, false, true},
		{"admin", domain.Actor{ID: "a", OrganizationID: "org", Active: true, Admin: true}, true, false, true},
		{"supervisor", domain.Actor{ID: "a", OrganizationID: "org", Active: true, Supervisor: true}, false, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if authz.Admin(tt.actor) != tt.admin || authz.Supervisor(tt.actor) != tt.supervisor || authz.Self(tt.actor, authz.Resource{UserID: "a", OrganizationID: "org"}) != tt.self {
				t.Fatal("unexpected role/self policy")
			}
			for _, target := range []authz.Resource{{UserID: "other", OrganizationID: "org"}, {UserID: "a", OrganizationID: "other"}, {}} {
				if authz.Self(tt.actor, target) {
					t.Fatal("non-self resource allowed")
				}
			}
		})
	}
}

func TestRelationshipsRequireSameOrgAndAcceptedLink(t *testing.T) {
	a := domain.Actor{ID: "a", OrganizationID: "org", Active: true, Supervisor: true, Admin: true}
	for _, target := range []authz.Resource{{UserID: "b", OrganizationID: "other"}, {UserID: "a", OrganizationID: "org"}, {}} {
		l := &links{allowed: true}
		if ok, _ := authz.Supervise(t.Context(), a, target, l); ok {
			t.Fatal("invalid supervisor access")
		}
		if ok, _ := authz.PeerSchedule(t.Context(), a, target, l); ok {
			t.Fatal("invalid peer access")
		}
		if l.called {
			t.Fatal("cross-org/self lookup reached storage")
		}
	}
	target := authz.Resource{UserID: "b", OrganizationID: "org"}
	for _, allowed := range []bool{false, true} {
		l := &links{allowed: allowed}
		if ok, _ := authz.Supervise(t.Context(), a, target, l); ok != allowed {
			t.Fatal("supervision did not match link")
		}
		if ok, _ := authz.PeerSchedule(t.Context(), a, target, l); ok != allowed {
			t.Fatal("peer access did not match link")
		}
	}
	a.Supervisor = false
	if ok, _ := authz.Supervise(t.Context(), a, target, &links{allowed: true}); ok {
		t.Fatal("admin bypassed supervision")
	}
	if ok, _ := authz.PeerSchedule(t.Context(), a, target, nil); ok {
		t.Fatal("nil relationship storage allowed")
	}
	a.Supervisor = true
	if ok, err := authz.Supervise(t.Context(), a, target, &links{allowed: true, err: errors.New("unavailable")}); ok || err == nil {
		t.Fatal("relationship error did not fail closed")
	}
}
