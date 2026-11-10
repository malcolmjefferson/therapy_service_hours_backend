package seed

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// testAccountSuffix is the reserved domain of the synthetic dev users (ADR-0011).
const testAccountSuffix = "@example.invalid"

// ApproveRequest names the synthetic test user whose pending entries are approved.
type ApproveRequest struct{ Email string }

// ApproveResult contains a count only, so no entry or user attributes are returned or logged.
type ApproveResult struct {
	EntriesApproved int64 `json:"entries_approved"`
}

// ApproveEntries is the dev-only stand-in for the review workflow until S7 exists (S3-07). It is
// refused outside dev and for any account that is not a synthetic test user, so a deployed prod
// environment and real people's entries can never be changed through it.
func (s *Service) ApproveEntries(ctx context.Context, request ApproveRequest) (ApproveResult, error) {
	if s.env != "dev" {
		return ApproveResult{}, domain.ErrForbidden
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || !strings.HasSuffix(email, testAccountSuffix) {
		return ApproveResult{}, domain.NewValidationError(domain.FieldError{Field: "approve_email", Message: "a synthetic test account is required"})
	}
	result, err := s.store.ApproveEntries(ctx, ApproveRequest{Email: email}, s.now().UTC())
	if err != nil {
		return ApproveResult{}, fmt.Errorf("approve seed entries: %w", err)
	}
	return result, nil
}
