package seed

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"unicode"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// BindingRequest supplies the expected identity through the IAM operator path.
// It is never echoed in responses or logs.
type BindingRequest struct {
	Email, Subject string
}

// BindingResult contains only snapshot counts, never user IDs or attributes.
type BindingResult struct {
	Users               int64 `json:"users"`
	SubjectUsers        int64 `json:"subject_users"`
	MatchingActiveAdmin int64 `json:"matching_active_admin"`
	AcceptedInvitations int64 `json:"accepted_invitations"`
}

// VerifyBinding checks existing dev state without applying a seed plan.
func (s *Service) VerifyBinding(ctx context.Context, request BindingRequest) (BindingResult, error) {
	if s.env != "dev" {
		return BindingResult{}, domain.ErrForbidden
	}
	request.Email = strings.ToLower(strings.TrimSpace(request.Email))
	address, err := mail.ParseAddress(request.Email)
	if err != nil || address.Address != request.Email || len(request.Email) > 254 || strings.HasSuffix(request.Email, "@example.invalid") {
		return BindingResult{}, domain.NewValidationError(domain.FieldError{Field: "admin_email", Message: "a valid non-test email is required"})
	}
	if request.Subject == "" || len(request.Subject) > 128 || strings.IndexFunc(request.Subject, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return BindingResult{}, domain.NewValidationError(domain.FieldError{Field: "expected_subject", Message: "a valid subject is required"})
	}
	result, err := s.store.VerifyBinding(ctx, request)
	if err != nil {
		return BindingResult{}, fmt.Errorf("verify identity binding: %w", err)
	}
	return result, nil
}
