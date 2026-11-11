package seed_test

import (
	"errors"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func TestApproveEntriesGuardsRunBeforeStorage(t *testing.T) {
	for _, tc := range []struct{ name, env, email string }{
		{"prod is refused", "prod", "test-invited@example.invalid"},
		{"empty email", "dev", ""},
		{"real account is refused", "dev", "admin@example.test"},
		{"look-alike domain is refused", "dev", "test@example.invalid.test"},
		{"display name", "dev", "Name <test-invited@example.invalid>"},
		{"oversized address", "dev", string(make([]byte, 300)) + "@example.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &store{}
			svc, err := seed.New(db, tc.env, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: tc.email}); err == nil || db.calls != 0 {
				t.Fatal("unsafe approval reached storage")
			}
		})
	}
}

func TestApproveEntriesProdIsForbidden(t *testing.T) {
	svc, _ := seed.New(&store{}, "prod", time.Now)
	if _, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "test-invited@example.invalid"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("got %v, want forbidden", err)
	}
}

func TestApproveEntriesNormalizesEmailAndUsesTheClock(t *testing.T) {
	db := &store{}
	at := time.Date(2026, 11, 10, 5, 0, 0, 0, time.FixedZone("x", 3600))
	svc, _ := seed.New(db, "dev", func() time.Time { return at })
	result, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: " Test-Invited@Example.Invalid "})
	if err != nil || result.EntriesApproved != 2 || db.approved.Email != "test-invited@example.invalid" || !db.approvedAt.Equal(at) || db.approvedAt.Location() != time.UTC {
		t.Fatalf("unexpected approval: %+v %+v %v", result, db.approved, err)
	}
}

func TestApproveEntriesWrapsStorageErrors(t *testing.T) {
	db := &store{err: domain.ErrConflict}
	svc, _ := seed.New(db, "dev", time.Now)
	result, err := svc.ApproveEntries(t.Context(), seed.ApproveRequest{Email: "test-invited@example.invalid"})
	if !errors.Is(err, domain.ErrConflict) || result != (seed.ApproveResult{}) {
		t.Fatal("failed approval returned a partial count")
	}
}
