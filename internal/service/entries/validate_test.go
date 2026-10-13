package entries_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// S2-04 tests: table-driven rules (GO-71). Notes and identifiers are fictional (CLAUDE.md §0.7).

const (
	category = "00000000-0000-4000-8000-0000000000c1"
	client1  = "00000000-0000-4000-8000-0000000000d1"
	client2  = "00000000-0000-4000-8000-0000000000d2"
)

func valid() entries.Input {
	return entries.Input{SessionDate: "2026-10-12", StartTime: "09:00", EndTime: "10:15", CategoryID: category, ClientIDs: []string{client1}, OnSite: true, Notes: "Pseudonym A"}
}

func with(mutate func(*entries.Input)) entries.Input {
	in := valid()
	mutate(&in)
	return in
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a *domain.ValidationError", err)
	}
	out := map[string]string{}
	for _, f := range verr.Fields {
		if _, dup := out[f.Field]; dup {
			t.Errorf("field %q reported twice", f.Field)
		}
		out[f.Field] = f.Message
	}
	return out
}

func TestValidate_AcceptsAValidEntry(t *testing.T) {
	t.Parallel()
	got, err := entries.Validate(valid())
	if err != nil {
		t.Fatal(err)
	}
	if !got.SessionDate.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", got.SessionDate)
	}
	if got.StartMinute != 540 || got.EndMinute != 615 || got.DurationMinutes != 75 || got.Hours() != 1.25 {
		t.Errorf("times = %d-%d, duration %d, hours %v", got.StartMinute, got.EndMinute, got.DurationMinutes, got.Hours())
	}
	if got.CategoryID != category || !slices.Equal(got.ClientIDs, []string{client1}) || !got.OnSite || got.Notes != "Pseudonym A" {
		t.Errorf("fields = %+v", got)
	}
}

func TestValidate_Times(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		start, end string
		wantMin    int32  // duration when valid
		wantField  string // field in error when invalid
	}{
		{name: "one quarter hour", start: "09:00", end: "09:15", wantMin: 15},
		{name: "ticket boundary: midnight start accepted", start: "00:00", end: "00:15", wantMin: 15},
		{name: "last quarter hour of the day", start: "23:30", end: "23:45", wantMin: 15},
		{name: "the whole day up to 23:45", start: "00:00", end: "23:45", wantMin: 1425},
		{name: "half and full hours", start: "13:30", end: "15:00", wantMin: 90},
		{name: "ticket boundary: 23:45 to 00:00 crosses midnight", start: "23:45", end: "00:00", wantField: "end_time"},
		{name: "end before start", start: "10:00", end: "09:45", wantField: "end_time"},
		{name: "end equals start", start: "10:00", end: "10:00", wantField: "end_time"},
		{name: "start off the step", start: "09:07", end: "10:00", wantField: "start_time"},
		{name: "end off the step", start: "09:00", end: "10:20", wantField: "end_time"},
		{name: "24:00 is not a time", start: "23:45", end: "24:00", wantField: "end_time"},
		{name: "minute 60", start: "09:60", end: "10:00", wantField: "start_time"},
		{name: "hour 25", start: "09:00", end: "25:00", wantField: "end_time"},
		{name: "missing leading zero", start: "9:00", end: "10:00", wantField: "start_time"},
		{name: "seconds are not accepted", start: "09:00:00", end: "10:00", wantField: "start_time"},
		{name: "surrounding space", start: " 09:00", end: "10:00", wantField: "start_time"},
		{name: "wrong separator", start: "09.00", end: "10:00", wantField: "start_time"},
		{name: "non digits", start: "0a:00", end: "10:00", wantField: "start_time"},
		{name: "empty start", start: "", end: "10:00", wantField: "start_time"},
		{name: "empty end", start: "09:00", end: "", wantField: "end_time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := entries.Validate(with(func(in *entries.Input) { in.StartTime, in.EndTime = tt.start, tt.end }))
			if tt.wantField == "" {
				if err != nil || got.DurationMinutes != tt.wantMin || got.EndMinute-got.StartMinute != tt.wantMin {
					t.Fatalf("got %+v, %v; want %d minutes", got, err, tt.wantMin)
				}
				return
			}
			fields := fieldsOf(t, err)
			if _, ok := fields[tt.wantField]; !ok || len(fields) != 1 {
				t.Fatalf("fields = %v, want only %s", fields, tt.wantField)
			}
		})
	}
}

func TestValidate_MidnightMessageIsClear(t *testing.T) {
	t.Parallel()
	_, err := entries.Validate(with(func(in *entries.Input) { in.StartTime, in.EndTime = "23:45", "00:00" }))
	if msg := fieldsOf(t, err)["end_time"]; !strings.Contains(msg, "cannot cross midnight") {
		t.Errorf("message = %q", msg)
	}
}

func TestHours(t *testing.T) {
	t.Parallel()
	tests := []struct {
		minutes int32
		want    float64
	}{{15, 0.25}, {30, 0.5}, {45, 0.75}, {60, 1}, {75, 1.25}, {90, 1.5}, {1425, 23.75}, {20, 0.33}, {40, 0.67}}
	for _, tt := range tests {
		if got := entries.HoursFor(tt.minutes); got != tt.want {
			t.Errorf("HoursFor(%d) = %v, want %v", tt.minutes, got, tt.want)
		}
	}
}

func TestValidate_SessionDate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		date string
		ok   bool
	}{
		{"2026-10-12", true},
		{"2028-02-29", true}, // leap day
		{"2026-02-29", false},
		{"2026-02-30", false},
		{"2026-13-01", false},
		{"2026-00-10", false},
		{"2026-10-00", false},
		{"20261012", false},
		{"2026-1-2", false},
		{"12/10/2026", false},
		{"2026-10-12T00:00:00Z", false},
		{" 2026-10-12", false},
		{"", false},
	}
	for _, tt := range tests {
		_, err := entries.Validate(with(func(in *entries.Input) { in.SessionDate = tt.date }))
		if tt.ok != (err == nil) {
			t.Errorf("date %q: err = %v, want ok=%v", tt.date, err, tt.ok)
		}
		if !tt.ok {
			if _, found := fieldsOf(t, err)["session_date"]; !found {
				t.Errorf("date %q: session_date not reported", tt.date)
			}
		}
	}
}

func TestValidate_Category(t *testing.T) {
	t.Parallel()
	got, err := entries.Validate(with(func(in *entries.Input) { in.CategoryID = strings.ToUpper(category) }))
	if err != nil || got.CategoryID != category {
		t.Fatalf("upper-case UUID: got %q, %v; want it lower-cased", got.CategoryID, err)
	}
	for _, id := range []string{"", "not-a-uuid", category + "0", category[:35], "00000000_0000_4000_8000_0000000000c1", "g0000000-0000-4000-8000-0000000000c1", "' OR 1=1 --"} {
		_, err := entries.Validate(with(func(in *entries.Input) { in.CategoryID = id }))
		if _, found := fieldsOf(t, err)["category_id"]; !found {
			t.Errorf("category %q accepted", id)
		}
	}
}

func TestValidate_Clients(t *testing.T) {
	t.Parallel()
	many := func(n int) []string {
		ids := make([]string, 0, n)
		for i := range n {
			ids = append(ids, fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1))
		}
		return ids
	}
	ok := map[string][]string{
		"none (nil)":   nil,
		"none (empty)": {},
		"two clients":  {client1, client2},
		"fifty":        many(50),
	}
	for name, ids := range ok {
		got, err := entries.Validate(with(func(in *entries.Input) { in.ClientIDs = ids }))
		if err != nil || len(got.ClientIDs) != len(ids) {
			t.Errorf("%s: got %v, %v", name, got.ClientIDs, err)
		}
	}
	if got, _ := entries.Validate(with(func(in *entries.Input) { in.ClientIDs = nil })); got.ClientIDs == nil {
		t.Error("no clients should be an empty slice, not nil")
	}
	bad := map[string][]string{
		"fifty-one":                   many(51),
		"duplicate":                   {client1, client1},
		"duplicate in different case": {client1, strings.ToUpper(client1)},
		"not a uuid":                  {client1, "nope"},
		"empty id":                    {""},
		"injection text":              {"'; DROP TABLE clients; --"},
	}
	for name, ids := range bad {
		_, err := entries.Validate(with(func(in *entries.Input) { in.ClientIDs = ids }))
		if _, found := fieldsOf(t, err)["client_ids"]; !found {
			t.Errorf("%s accepted", name)
		}
	}
	got, _ := entries.Validate(with(func(in *entries.Input) { in.ClientIDs = []string{client2, client1} }))
	if !slices.Equal(got.ClientIDs, []string{client2, client1}) {
		t.Errorf("request order lost: %v", got.ClientIDs)
	}
}

func TestValidate_Notes(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"empty":                  "",
		"only spaces":            "   ",
		"trimmed":                "  Pseudonym A  ",
		"line breaks and tabs":   "Line one\r\nLine two\tend",
		"exactly 2000":           strings.Repeat("x", 2000),
		"2000 multi-byte":        strings.Repeat("名", 2000),
		"sql-looking text":       "'; DROP TABLE entries; --",
		"percent and underscore": "100% _done_",
	}
	for name, notes := range ok {
		got, err := entries.Validate(with(func(in *entries.Input) { in.Notes = notes }))
		if err != nil || got.Notes != strings.TrimSpace(notes) {
			t.Errorf("%s: got %q, %v", name, got.Notes, err)
		}
	}
	bad := map[string]string{
		"2001 characters":  strings.Repeat("x", 2001),
		"2001 multi-byte":  strings.Repeat("名", 2001),
		"NUL":              "a\x00b",
		"escape":           "a\x1bb",
		"invalid UTF-8":    "a\xffb",
		"unicode control":  "a\u0085b",
		"2000 plus spaces": strings.Repeat("x", 2001) + "   ",
	}
	for name, notes := range bad {
		_, err := entries.Validate(with(func(in *entries.Input) { in.Notes = notes }))
		if _, found := fieldsOf(t, err)["notes"]; !found {
			t.Errorf("%s accepted", name)
		}
	}
	// Surrounding space does not count toward the limit.
	if _, err := entries.Validate(with(func(in *entries.Input) { in.Notes = "  " + strings.Repeat("x", 2000) + "  " })); err != nil {
		t.Errorf("2000 characters inside spaces: %v", err)
	}
}

func TestValidate_ReportsEveryInvalidFieldAtOnce(t *testing.T) {
	t.Parallel()
	_, err := entries.Validate(entries.Input{SessionDate: "x", StartTime: "y", EndTime: "z", CategoryID: "c", ClientIDs: []string{"d"}, Notes: strings.Repeat("n", 2001)})
	fields := fieldsOf(t, err)
	for _, want := range []string{"session_date", "start_time", "end_time", "category_id", "client_ids", "notes"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("%s not reported (got %v)", want, fields)
		}
	}
	if _, err := entries.Validate(entries.Input{}); err == nil {
		t.Error("an empty input is valid")
	}
}

func TestValidate_NeverEchoesSubmittedValues(t *testing.T) {
	t.Parallel()
	secret := "hunter2-secret"
	_, err := entries.Validate(entries.Input{SessionDate: secret, StartTime: secret, EndTime: secret, CategoryID: secret, ClientIDs: []string{secret}, Notes: secret + "\x00"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoes input: %v", err)
	}
}

func TestValidate_DoesNotChangeItsInput(t *testing.T) {
	t.Parallel()
	in := valid()
	in.ClientIDs = []string{strings.ToUpper(client1)}
	if _, err := entries.Validate(in); err != nil {
		t.Fatal(err)
	}
	if in.ClientIDs[0] != strings.ToUpper(client1) {
		t.Error("Validate modified the caller's slice")
	}
}
