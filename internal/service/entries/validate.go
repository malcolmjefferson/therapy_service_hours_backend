// Package entries holds the business rules for hours entries. Validate (S2-04) turns the editable
// fields of a create or update request into values that are safe to store, and calculates the
// duration. It performs no I/O: the database repeats the time rules as constraints (ARC-21), and
// the checks that need storage (the category exists, the clients belong to the caller, no overlap)
// belong to the store behind the endpoints (S2-05).
package entries

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

const (
	// StepMinutes is the granularity of start and end times (ASM-02).
	StepMinutes = 15
	// MaxNotesLength is the longest notes text in characters (SEC-26); the column checks the same limit.
	MaxNotesLength = 2000
	// MaxClients is the most clients one entry can carry, matching the spec.
	MaxClients = 50
)

// Input is the editable part of an entry as it arrives in a create or full-replacement request.
// It deliberately has no hours or duration: the server calculates them (FR-LOG-02). Times are
// local clock times `HH:MM` with no timezone (CLAUDE.md §7.1).
type Input struct {
	SessionDate string // YYYY-MM-DD
	StartTime   string // HH:MM
	EndTime     string // HH:MM
	CategoryID  string
	ClientIDs   []string
	OnSite      bool
	Notes       string
}

// Validated is an Input that passed every rule, normalized for storage.
type Validated struct {
	// SessionDate is midnight UTC on the session date; only the date part is meaningful.
	SessionDate time.Time
	// StartMinute and EndMinute are minutes after midnight, in 15-minute steps, with EndMinute > StartMinute.
	StartMinute, EndMinute int32
	// DurationMinutes is EndMinute - StartMinute, between 15 and 1425.
	DurationMinutes int32
	CategoryID      string
	// ClientIDs are lower-case UUIDs in request order, without duplicates.
	ClientIDs []string
	OnSite    bool
	// Notes is trimmed of surrounding space; empty means no notes.
	Notes string
}

// Hours returns the duration in hours to two decimals, for example 1.25 for 75 minutes (ASM-06).
// Quarter-hour durations are exact in floating point.
func (v Validated) Hours() float64 { return HoursFor(v.DurationMinutes) }

// HoursFor converts minutes to hours rounded to two decimals.
func HoursFor(minutes int32) float64 {
	return math.Round(float64(minutes)/60*100) / 100
}

// Validate checks every rule and reports all invalid fields at once, so a form can mark them
// together. The error is a *domain.ValidationError; its messages never contain the submitted values.
func Validate(in Input) (Validated, error) {
	var (
		out    Validated
		fields []domain.FieldError
	)
	bad := func(field, message string) {
		fields = append(fields, domain.FieldError{Field: field, Message: message})
	}

	if date, ok := parseDate(in.SessionDate); ok {
		out.SessionDate = date
	} else {
		bad("session_date", "must be a valid date in the form YYYY-MM-DD")
	}

	start, startOK := parseClock(in.StartTime)
	end, endOK := parseClock(in.EndTime)
	switch {
	case !startOK:
		bad("start_time", "must be a time in the form HH:MM")
	case start%StepMinutes != 0:
		bad("start_time", "must be on a 15-minute step (:00, :15, :30 or :45)")
	}
	switch {
	case !endOK:
		bad("end_time", "must be a time in the form HH:MM")
	case end%StepMinutes != 0:
		bad("end_time", "must be on a 15-minute step (:00, :15, :30 or :45)")
	}
	if startOK && endOK && start%StepMinutes == 0 && end%StepMinutes == 0 {
		if end <= start {
			// 23:45 to 00:00 lands here: the end is before the start, because a session cannot cross midnight.
			bad("end_time", "must be after start_time; a session cannot cross midnight")
		} else {
			out.StartMinute, out.EndMinute, out.DurationMinutes = start, end, end-start
		}
	}

	if id, ok := canonicalUUID(in.CategoryID); ok {
		out.CategoryID = id
	} else {
		bad("category_id", "must be a category identifier")
	}

	clients, clientErr := validateClients(in.ClientIDs)
	if clientErr != "" {
		bad("client_ids", clientErr)
	}
	out.ClientIDs = clients

	notes, notesErr := validateNotes(in.Notes)
	if notesErr != "" {
		bad("notes", notesErr)
	}
	out.Notes = notes
	out.OnSite = in.OnSite

	if len(fields) > 0 {
		return Validated{}, domain.NewValidationError(fields...)
	}
	return out, nil
}

// parseDate accepts only a real calendar date written YYYY-MM-DD.
func parseDate(s string) (time.Time, bool) {
	if len(s) != len("2006-01-02") {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

// parseClock accepts only HH:MM from 00:00 to 23:59 and returns minutes after midnight.
func parseClock(s string) (int32, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, okH := twoDigits(s[0:2])
	m, okM := twoDigits(s[3:5])
	if !okH || !okM || h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func twoDigits(s string) (int32, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int32(s[0]-'0')*10 + int32(s[1]-'0'), true
}

// canonicalUUID accepts the 36-character hyphenated form and returns it in lower case.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return "", false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return "", false
		}
	}
	return strings.ToLower(s), true
}

func validateClients(ids []string) ([]string, string) {
	if len(ids) > MaxClients {
		return nil, fmt.Sprintf("must list at most %d clients", MaxClients)
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id, ok := canonicalUUID(raw)
		if !ok {
			return nil, "must contain only client identifiers"
		}
		if _, dup := seen[id]; dup {
			return nil, "must not list a client twice"
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, ""
}

// validateNotes allows free text, including line breaks and tabs, but no other control characters.
func validateNotes(raw string) (string, string) {
	notes := strings.TrimSpace(raw)
	switch {
	case !utf8.ValidString(notes):
		return "", "must be valid text"
	case utf8.RuneCountInString(notes) > MaxNotesLength:
		return "", fmt.Sprintf("must be at most %d characters", MaxNotesLength)
	case strings.ContainsFunc(notes, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }):
		return "", "must not contain control characters"
	}
	return notes, ""
}
