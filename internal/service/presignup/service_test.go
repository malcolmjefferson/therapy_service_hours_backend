package presignup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/presignup"
)

type reader struct {
	result presignup.Admission
	err    error
	email  string
	at     time.Time
	calls  int
}

func (r *reader) Admission(_ context.Context, email string, at time.Time) (presignup.Admission, error) {
	r.email, r.at = email, at
	r.calls++
	return r.result, r.err
}

func TestAdmissionPolicy(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, email          string
		invited, deactivated bool
		denied               bool
	}{
		{"pending invite", "Pseudonym-A@example.test", true, false, false},
		{"uninvited", "pseudonym-b@example.test", false, false, true},
		{"deactivated", "pseudonym-c@example.test", true, true, true},
		{"empty", "", true, false, true},
		{"display name", "Pseudonym <pseudonym@example.test>", true, false, true},
		{"injection", "' OR 1=1 --", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &reader{result: presignup.Admission{Invited: tc.invited, Deactivated: tc.deactivated}}
			svc, err := presignup.New(store, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			err = svc.Check(t.Context(), tc.email)
			if errors.Is(err, domain.ErrForbidden) != tc.denied || (!tc.denied && err != nil) {
				t.Fatalf("unexpected decision: %v", err)
			}
			if store.calls > 0 && (store.email != tc.email || store.at != at) {
				t.Fatal("email/clock not passed to reader")
			}
		})
	}
}

func TestDatabaseFailureAndConstructorValidation(t *testing.T) {
	failure := errors.New("database failure")
	svc, _ := presignup.New(&reader{err: failure}, time.Now)
	if err := svc.Check(t.Context(), "pseudonym@example.test"); !errors.Is(err, failure) {
		t.Fatalf("database error lost: %v", err)
	}
	if _, err := presignup.New(nil, time.Now); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := presignup.New(&reader{}, nil); err == nil {
		t.Fatal("nil clock accepted")
	}
}
