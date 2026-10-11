// Package presignup enforces the read-only invitation gate before Cognito creates an identity.
package presignup

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Admission describes the two facts needed by the allowlist gate, from one database snapshot.
type Admission struct{ Invited, Deactivated bool }

// Reader supplies admission facts without writing or reading unrelated user data.
type Reader interface {
	Admission(context.Context, string, time.Time) (Admission, error)
}

// Service owns the invitation decision; identity binding and acceptance belong to S1-06.
type Service struct {
	reader Reader
	now    func() time.Time
}

// New validates and injects the read-only store and clock.
func New(reader Reader, now func() time.Time) (*Service, error) {
	if reader == nil || now == nil {
		return nil, fmt.Errorf("pre-sign-up: reader and clock are required")
	}
	return &Service{reader: reader, now: now}, nil
}

// Check admits only a syntactically valid email with a pending unexpired invitation
// and no deactivated user. All policy denials share ErrForbidden to avoid enumerating accounts.
func (s *Service) Check(ctx context.Context, email string) error {
	email = strings.TrimSpace(email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return domain.ErrForbidden
	}
	admission, err := s.reader.Admission(ctx, email, s.now())
	if err != nil {
		return fmt.Errorf("read pre-sign-up admission: %w", err)
	}
	if !admission.Invited || admission.Deactivated {
		return domain.ErrForbidden
	}
	return nil
}
