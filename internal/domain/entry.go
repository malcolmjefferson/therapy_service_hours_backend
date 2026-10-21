package domain

import (
	"math"
	"time"
)

// CategoryGroup is the group a category belongs to. It decides which goals the hours count toward.
type CategoryGroup string

// The three category groups (CLAUDE.md §8).
const (
	GroupDirect      CategoryGroup = "direct"
	GroupIndirect    CategoryGroup = "indirect"
	GroupSupervision CategoryGroup = "supervision"
)

// ParseCategoryGroup returns the group named s, or false when s is not one of the three.
func ParseCategoryGroup(s string) (CategoryGroup, bool) {
	switch g := CategoryGroup(s); g {
	case GroupDirect, GroupIndirect, GroupSupervision:
		return g, true
	default:
		return "", false
	}
}

// EntryStatus is where an entry is in the review lifecycle (CLAUDE.md §7.1).
type EntryStatus string

// The review statuses.
const (
	StatusPending  EntryStatus = "pending"
	StatusApproved EntryStatus = "approved"
	StatusRejected EntryStatus = "rejected"
)

// EntrySort is a column the entries table can be sorted by. The values are the API names.
type EntrySort string

// The sortable columns (FR-LOG-08). Adding one also needs two list queries (GO-14, SEC-24).
const (
	SortSessionDate EntrySort = "session_date"
	SortCategory    EntrySort = "category"
	SortClient      EntrySort = "client"
	SortHours       EntrySort = "hours"
	SortOnSite      EntrySort = "on_site"
)

// ParseEntrySort returns the sort column named s, or false when s is not on the allowlist.
func ParseEntrySort(s string) (EntrySort, bool) {
	switch c := EntrySort(s); c {
	case SortSessionDate, SortCategory, SortClient, SortHours, SortOnSite:
		return c, true
	default:
		return "", false
	}
}

// SortDirection is ascending or descending.
type SortDirection string

// The two directions.
const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

// ParseSortDirection returns the direction named s, or false when s is neither "asc" nor "desc".
func ParseSortDirection(s string) (SortDirection, bool) {
	switch d := SortDirection(s); d {
	case SortAsc, SortDesc:
		return d, true
	default:
		return "", false
	}
}

// CategoryRef names the category of an entry together with its group.
type CategoryRef struct {
	ID    string
	Name  string
	Group CategoryGroup
}

// ClientRef is a saved client on an entry. Names are pseudonyms or initials only (CLAUDE.md §0.7).
type ClientRef struct {
	ID   string
	Name string
}

// Entry is one logged session, as its owner sees it.
type Entry struct {
	ID string
	// SessionDate is the calendar day of the session; only the date part is meaningful.
	SessionDate time.Time
	// StartTime and EndTime are local clock times as "HH:MM". Both are empty for a legacy entry
	// imported without times, which has NeedsTime set.
	StartTime, EndTime string
	DurationMinutes    int
	Category           CategoryRef
	// Clients are the saved clients on the entry, ordered by name.
	Clients []ClientRef
	OnSite  bool
	// Notes is empty when the entry has none.
	Notes     string
	Status    EntryStatus
	NeedsTime bool
	// ReviewNote is the reviewer's reason; empty unless the entry was rejected.
	ReviewNote string
	// ReviewedAt is nil until a supervisor reviews the entry.
	ReviewedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Hours is the duration in hours, rounded to two decimals as shown to users (CLAUDE.md §7.1). The
// server derives it from DurationMinutes; a client never supplies it (GO-55).
func (e Entry) Hours() float64 {
	return math.Round(float64(e.DurationMinutes)/60*100) / 100
}

// ConflictingEntry is the caller's own session that a new or edited session overlaps. It carries only
// what the message needs to name the clash, so it can go back to its owner without the notes or clients.
type ConflictingEntry struct {
	ID          string
	SessionDate time.Time
	// StartTime and EndTime are local clock times as "HH:MM".
	StartTime, EndTime string
}

// EntryOverlapError means the session overlaps another of the user's non-rejected sessions
// (FR-LOG-04). It carries the clash so the form can say which session it is. It matches ErrConflict.
type EntryOverlapError struct {
	Conflicting ConflictingEntry
}

// Error reports the conflict without the session details, which are user data.
func (e *EntryOverlapError) Error() string { return "entry overlaps another entry" }

// Is makes the error match ErrConflict.
func (e *EntryOverlapError) Is(target error) bool { return target == ErrConflict }

// EntryLockedError means the entry is approved and cannot be edited or deleted until a supervisor
// reverts the approval (CLAUDE.md §7.1). It matches ErrConflict.
type EntryLockedError struct{}

// Error says why the entry cannot change.
func (e *EntryLockedError) Error() string { return "entry is approved and locked" }

// Is makes the error match ErrConflict.
func (e *EntryLockedError) Is(target error) bool { return target == ErrConflict }
