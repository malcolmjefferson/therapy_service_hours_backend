package seed_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

type bindingStore struct {
	store
	request seed.BindingRequest
	reads   int
}

func (s *bindingStore) VerifyBinding(_ context.Context, request seed.BindingRequest) (seed.BindingResult, error) {
	s.reads++
	s.request = request
	return seed.BindingResult{Users: 1}, s.err
}

func TestBindingVerificationGuardsBeforeAnyStorage(t *testing.T) {
	for _, tc := range []struct{ env, email, subject string }{
		{"prod", "admin@example.test", "Subject"},
		{"dev", "", "Subject"},
		{"dev", "Name <admin@example.test>", "Subject"},
		{"dev", "test-invited@example.invalid", "Subject"},
		{"dev", "admin@example.test", ""},
		{"dev", "admin@example.test", " Subject"},
		{"dev", "admin@example.test", "Subject\n"},
		{"dev", "admin@example.test", strings.Repeat("s", 129)},
	} {
		db := &bindingStore{}
		svc, _ := seed.New(db, tc.env, time.Now)
		if _, err := svc.VerifyBinding(t.Context(), seed.BindingRequest{Email: tc.email, Subject: tc.subject}); err == nil || db.reads != 0 || db.calls != 0 {
			t.Fatal("invalid verification reached storage")
		}
	}
}

func TestBindingVerificationUsesReadPathAndPreservesSubject(t *testing.T) {
	db := &bindingStore{}
	svc, _ := seed.New(db, "dev", time.Now)
	result, err := svc.VerifyBinding(t.Context(), seed.BindingRequest{Email: " Admin@Example.Test ", Subject: "SubjectCase"})
	if err != nil || result.Users != 1 || db.reads != 1 || db.calls != 0 || db.request.Email != "admin@example.test" || db.request.Subject != "SubjectCase" {
		t.Fatal("verification seeded data or changed the expected subject")
	}
	db.err = domain.ErrConflict
	result, err = svc.VerifyBinding(t.Context(), seed.BindingRequest{Email: "admin@example.test", Subject: "SubjectCase"})
	if !errors.Is(err, domain.ErrConflict) || result != (seed.BindingResult{}) {
		t.Fatal("failed verification returned partial counts")
	}
}

func TestVerificationEventFailsClosedOnLegacySeedShape(t *testing.T) {
	var legacy seed.Request
	if err := json.Unmarshal([]byte(`{"operation":"verify-binding","verification_email":"admin@example.test","expected_subject":"Subject"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	db := &store{}
	svc, _ := seed.New(db, "dev", time.Now)
	if _, err := svc.Run(t.Context(), legacy); err == nil || db.calls != 0 {
		t.Fatal("verification event could seed identities on an older binary")
	}
}
