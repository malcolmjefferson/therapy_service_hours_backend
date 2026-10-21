package domain_test

import (
	"errors"
	"fmt"
	"strings"
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

func TestEntryConflictErrors_MatchErrConflictAndKeepTheirDetail(t *testing.T) {
	t.Parallel()
	clash := domain.ConflictingEntry{ID: "id", StartTime: "09:00", EndTime: "10:00"}
	overlap := fmt.Errorf("create entry: %w", &domain.EntryOverlapError{Conflicting: clash})
	locked := fmt.Errorf("update entry: %w", &domain.EntryLockedError{})
	for name, err := range map[string]error{"overlap": overlap, "locked": locked} {
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("%s does not match ErrConflict", name)
		}
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s matches an unrelated error", name)
		}
	}
	var got *domain.EntryOverlapError
	if !errors.As(overlap, &got) || got.Conflicting != clash {
		t.Errorf("errors.As lost the clash: %+v", got)
	}
	if strings.Contains(overlap.Error(), "09:00") {
		t.Errorf("the message carries session data: %q", overlap.Error())
	}
	if errors.As(locked, &got) {
		t.Error("a locked entry matches the overlap error")
	}
}
