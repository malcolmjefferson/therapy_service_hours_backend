package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

func TestValidationError_ListsEveryFieldAndMatchesWithErrorsAs(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("create entry: %w", domain.NewValidationError(
		domain.FieldError{Field: "start_time", Message: "must be on a 15-minute step"},
		domain.FieldError{Field: "end_time", Message: "must be after start_time"},
	))
	want := "create entry: validation failed: start_time: must be on a 15-minute step; end_time: must be after start_time"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 2 {
		t.Errorf("errors.As failed or lost fields: %v", verr)
	}
}
