package entries_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
)

// S4-05 end-to-end tests: a legacy "Needs time" entry (imported without times, FR-IO-02) is completed
// through PUT /entries/{id}, through the real router, service and PostgreSQL store with the real
// migrations. Names and notes are fictional pseudonyms (CLAUDE.md §0.7).

// legacyState reads what is stored for one entry: the flag, the minutes, whether it has times and its status.
type legacyState struct {
	NeedsTime bool
	Minutes   int
	HasTimes  bool
	Status    string
}

func (e *env) legacyState(id string) legacyState {
	e.t.Helper()
	var s legacyState
	err := e.conn.QueryRow(e.t.Context(),
		`SELECT needs_time, duration_minutes, start_time IS NOT NULL AND end_time IS NOT NULL, status FROM entries WHERE id = $1`, id).
		Scan(&s.NeedsTime, &s.Minutes, &s.HasTimes, &s.Status)
	if err != nil {
		e.t.Fatalf("read entry %s: %v", id, err)
	}
	return s
}

// assertFields fails for each key of want that the entry does not carry with that value.
func assertFields(t *testing.T, got, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

// getEntry reads one entry as e.actor and decodes the owner view.
func (e *env) getEntry(id string) map[string]any {
	e.t.Helper()
	status, body := e.call(http.MethodGet, "/entries/"+id, "")
	if status != http.StatusOK {
		e.t.Fatalf("GET /entries/%s = %d: %s", id, status, body)
	}
	return decode[map[string]any](e.t, body)
}

func TestUpdateNeedsTimeEntry_AddingTimesClearsTheFlagAndCalculatesTheHours(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	one, two := e.client(a, "Pseudonym One"), e.client(a, "Pseudonym Two")
	// An imported ledger row: 1.5 hours typed in the prototype, no times, two clients and a note.
	id := e.entry(a, row{date: "2026-03-02", minutes: 90, category: 2, onSite: true, notes: "Imported note", clients: []string{one, two}})
	e.actor = a

	before := e.getEntry(id)
	assertFields(t, before, map[string]any{"needs_time": true, "hours": 1.5, "duration_minutes": float64(90)})
	for _, absent := range []string{"start_time", "end_time"} {
		if _, present := before[absent]; present {
			t.Errorf("legacy entry has %q: %v", absent, before)
		}
	}

	// The times decide the length: 09:00 to 10:15 is 1.25 hours, not the imported 1.5.
	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-03-02", "09:00", "10:15", 2, `"on_site":true`, `"notes":"Imported note"`, clientIDs(one, two)))
	if status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	got := decode[map[string]any](t, body)
	assertFields(t, got, map[string]any{
		"id": id, "session_date": "2026-03-02", "start_time": "09:00", "end_time": "10:15",
		"duration_minutes": float64(75), "hours": 1.25, "needs_time": false, "on_site": true, "status": "pending", "notes": "Imported note",
	})
	if len(got["clients"].([]any)) != 2 {
		t.Errorf("clients = %v, want both kept", got["clients"])
	}
	if s := e.legacyState(id); s != (legacyState{NeedsTime: false, Minutes: 75, HasTimes: true, Status: "pending"}) {
		t.Errorf("stored = %+v", s)
	}

	// The change is what a later read, and the list, show.
	assertFields(t, e.getEntry(id), map[string]any{"needs_time": false, "hours": 1.25, "start_time": "09:00"})
	status, listed := e.get(url.Values{})
	if status != http.StatusOK {
		t.Fatalf("GET /entries = %d: %s", status, listed)
	}
	items := decode[listBody](t, listed).Items
	if len(items) != 1 {
		t.Fatalf("list = %v", items)
	}
	assertFields(t, items[0], map[string]any{"needs_time": false, "hours": 1.25})
}

func TestUpdateNeedsTimeEntry_HoursCanGrowAsWellAsShrink(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	id := e.entry(a, row{date: "2026-03-02", minutes: 30})
	e.actor = a
	status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-03-02", "13:00", "16:00", 1))
	if status != http.StatusOK {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	if got := decode[map[string]any](t, body); got["hours"] != 3.0 || got["needs_time"] != false {
		t.Errorf("entry = %v", got)
	}
}

func TestUpdateNeedsTimeEntry_SavingWithoutBothTimesIsRefusedAndTheEntryStaysLegacy(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	id := e.entry(a, row{date: "2026-03-02", minutes: 90, notes: "Imported note"})
	e.actor = a
	category := e.category(1)

	for name, tc := range map[string]struct {
		body   string
		fields []string
	}{
		"no times": {`{"session_date":"2026-03-02","category_id":"` + category + `"}`, nil},
		"empty times": {
			`{"session_date":"2026-03-02","start_time":"","end_time":"","category_id":"` + category + `"}`, nil,
		},
		"null times": {
			`{"session_date":"2026-03-02","start_time":null,"end_time":null,"category_id":"` + category + `"}`, nil,
		},
		"only a start": {
			`{"session_date":"2026-03-02","start_time":"09:00","category_id":"` + category + `"}`, nil,
		},
		"only an end": {
			`{"session_date":"2026-03-02","end_time":"10:00","category_id":"` + category + `"}`, nil,
		},
		"a start off the 15-minute step": {
			`{"session_date":"2026-03-02","start_time":"09:10","end_time":"10:00","category_id":"` + category + `"}`, []string{"start_time"},
		},
		"an end before the start": {
			`{"session_date":"2026-03-02","start_time":"10:00","end_time":"09:00","category_id":"` + category + `"}`, []string{"end_time"},
		},
		"an end that crosses midnight": {
			`{"session_date":"2026-03-02","start_time":"23:45","end_time":"00:00","category_id":"` + category + `"}`, []string{"end_time"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			status, res := e.call(http.MethodPut, "/entries/"+id, tc.body)
			p := expectProblem(t, status, res, http.StatusBadRequest, "validation")
			if tc.fields != nil {
				var got []string
				for _, f := range p.Errors {
					got = append(got, f.Field)
				}
				if !slices.Equal(got, tc.fields) {
					t.Errorf("fields = %v, want %v", got, tc.fields)
				}
			}
			if s := e.legacyState(id); s != (legacyState{NeedsTime: true, Minutes: 90, HasTimes: false, Status: "pending"}) {
				t.Errorf("entry changed by a refused save: %+v", s)
			}
		})
	}
}

func TestUpdateNeedsTimeEntry_NeverClashesWithOtherLegacyEntriesOrRejectedSessions(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	// Three legacy entries and a rejected session share the day; none of them has a window to clash with.
	first := e.entry(a, row{date: "2026-03-02", minutes: 60})
	second := e.entry(a, row{date: "2026-03-02", minutes: 120})
	third := e.entry(a, row{date: "2026-03-02", minutes: 45})
	e.entry(a, row{date: "2026-03-02", start: "09:00", end: "10:00", status: "rejected"})
	e.actor = a

	// Completing them one after another, each into a different window, works, and back-to-back is allowed.
	for id, w := range map[string][2]string{first: {"09:00", "10:00"}, second: {"10:00", "12:00"}, third: {"12:00", "12:45"}} {
		if status, body := e.call(http.MethodPut, "/entries/"+id, e.body("2026-03-02", w[0], w[1], 1)); status != http.StatusOK {
			t.Fatalf("PUT %s = %d: %s", id, status, body)
		}
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND needs_time`, a.ID); n != 0 {
		t.Errorf("%d entries still need time", n)
	}
}

func TestUpdateNeedsTimeEntry_TimesThatOverlapASessionAreRefusedAndTheEntryStaysLegacy(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	timed := e.entry(a, row{date: "2026-03-02", start: "09:00", end: "10:00"})
	e.entry(b, row{date: "2026-03-02", start: "13:00", end: "14:00"}) // Another user's session never clashes.
	legacy := e.entry(a, row{date: "2026-03-02", minutes: 90})
	e.actor = a

	status, body := e.call(http.MethodPut, "/entries/"+legacy, e.body("2026-03-02", "09:30", "10:30", 1))
	p := expectProblem(t, status, body, http.StatusConflict, "entry-overlap")
	if p.ConflictingEntry["id"] != timed || p.ConflictingEntry["start_time"] != "09:00" || p.ConflictingEntry["end_time"] != "10:00" {
		t.Errorf("conflicting_entry = %v, want the 09:00 to 10:00 session", p.ConflictingEntry)
	}
	if s := e.legacyState(legacy); s != (legacyState{NeedsTime: true, Minutes: 90, HasTimes: false, Status: "pending"}) {
		t.Errorf("entry changed by a refused save: %+v", s)
	}

	// The same legacy entry saves fine into a free window, and into the other user's busy one.
	if status, body := e.call(http.MethodPut, "/entries/"+legacy, e.body("2026-03-02", "13:00", "14:30", 1)); status != http.StatusOK {
		t.Fatalf("PUT into a free window = %d: %s", status, body)
	}
}

func TestUpdateNeedsTimeEntry_OnceTimedItStaysCheckedForOverlap(t *testing.T) {
	e := newEnv(t)
	a := e.user("a@example.test")
	legacy := e.entry(a, row{date: "2026-03-02", minutes: 60})
	other := e.entry(a, row{date: "2026-03-02", start: "11:00", end: "12:00"})
	e.actor = a

	if status, body := e.call(http.MethodPut, "/entries/"+legacy, e.body("2026-03-02", "09:00", "10:00", 1)); status != http.StatusOK {
		t.Fatalf("adding times = %d: %s", status, body)
	}
	// Moving the now-timed entry over the other session is an overlap, like any timed entry.
	status, body := e.call(http.MethodPut, "/entries/"+legacy, e.body("2026-03-02", "10:30", "11:30", 1))
	p := expectProblem(t, status, body, http.StatusConflict, "entry-overlap")
	if p.ConflictingEntry["id"] != other {
		t.Errorf("conflicting_entry = %v, want %s", p.ConflictingEntry, other)
	}
	if s := e.legacyState(legacy); s != (legacyState{NeedsTime: false, Minutes: 60, HasTimes: true, Status: "pending"}) {
		t.Errorf("stored = %+v", s)
	}
}

func TestUpdateNeedsTimeEntry_OwnershipAndLockingStillApply(t *testing.T) {
	e := newEnv(t)
	a, b := e.user("a@example.test"), e.user("b@example.test")
	theirs := e.entry(b, row{date: "2026-03-02", minutes: 90})
	approved := e.entry(a, row{date: "2026-03-03", minutes: 90, status: "approved"})
	e.actor = a

	status, body := e.call(http.MethodPut, "/entries/"+theirs, e.body("2026-03-02", "09:00", "10:00", 1))
	expectProblem(t, status, body, http.StatusNotFound, "not-found")
	if s := e.legacyState(theirs); s != (legacyState{NeedsTime: true, Minutes: 90, HasTimes: false, Status: "pending"}) {
		t.Errorf("another user's legacy entry changed: %+v", s)
	}

	status, body = e.call(http.MethodPut, "/entries/"+approved, e.body("2026-03-03", "09:00", "10:00", 1))
	expectProblem(t, status, body, http.StatusConflict, "entry-locked")
	if s := e.legacyState(approved); s != (legacyState{NeedsTime: true, Minutes: 90, HasTimes: false, Status: "approved"}) {
		t.Errorf("an approved legacy entry changed: %+v", s)
	}
}
