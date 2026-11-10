package loghoursqueries_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	lq "github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
)

// S3-02 tests: GetProgressMinutes against real PostgreSQL with the real migrations (GO-72). The expected
// numbers are worked out by hand in the comments, not computed by the query's own logic.

// category display positions (CLAUDE.md §8) used below.
const (
	individualCounseling  = 1  // direct
	groupFacilitation     = 2  // direct
	progressNotes         = 6  // indirect
	staffMeetings         = 7  // indirect
	groupSupervision      = 11 // supervision, the group half of the split
	individualSupervision = 12 // supervision, the individual half of the split
)

func (f *fixture) progress(t *testing.T, user pgtype.UUID) lq.GetProgressMinutesRow {
	t.Helper()
	got, err := f.q.GetProgressMinutes(f.ctx, user)
	if err != nil {
		t.Fatalf("GetProgressMinutes: %v", err)
	}
	return got
}

// logged inserts a legacy entry without times (needs time) in the given status, so fixtures never collide on the overlap rule.
func (f *fixture) logged(t *testing.T, user pgtype.UUID, status string, category int, minutes int32, onSite bool) lq.Entry {
	t.Helper()
	e := f.entry(t, user, spec{date: "2026-10-01", minutes: minutes, category: category, onSite: onSite})
	if status != "pending" {
		f.setStatus(t, e.ID, status)
	}
	return e
}

// seedHandComputed inserts the twelve entries of the hand-computed fixture for one user.
func (f *fixture) seedHandComputed(t *testing.T, user pgtype.UUID) {
	t.Helper()
	f.logged(t, user, "approved", individualCounseling, 90, true)
	f.logged(t, user, "approved", groupFacilitation, 60, false)
	f.logged(t, user, "pending", individualCounseling, 45, true)
	f.logged(t, user, "rejected", individualCounseling, 120, true) // excluded
	f.logged(t, user, "approved", progressNotes, 30, true)
	f.logged(t, user, "pending", staffMeetings, 15, false)
	f.logged(t, user, "rejected", progressNotes, 60, false) // excluded
	f.logged(t, user, "approved", groupSupervision, 75, false)
	f.logged(t, user, "pending", groupSupervision, 30, true)
	f.logged(t, user, "approved", individualSupervision, 60, false)
	f.logged(t, user, "pending", individualSupervision, 45, false)
	f.logged(t, user, "rejected", individualSupervision, 60, false) // excluded
}

func TestGetProgressMinutes_MatchesTheHandComputedFixture(t *testing.T) {
	f := newFixture(t)
	user := f.user(t, "progress@example.test")
	f.seedHandComputed(t, user)

	want := lq.GetProgressMinutesRow{
		// Direct: approved 90 + 60; pending 45; the rejected 120 is left out.
		DirectApprovedMinutes: 150, DirectPendingMinutes: 45,
		// Supervision: approved 75 + 60; pending 30 + 45; the rejected 60 is left out.
		SupervisionApprovedMinutes: 135, SupervisionPendingMinutes: 75,
		// The split: group 75 / 30, individual 60 / 45.
		SupervisionGroupApprovedMinutes: 75, SupervisionGroupPendingMinutes: 30,
		SupervisionIndividualApprovedMinutes: 60, SupervisionIndividualPendingMinutes: 45,
		// Total: approved 90 + 60 + 30 + 75 + 60 = 315; pending 45 + 15 + 30 + 45 = 135.
		TotalActivityApprovedMinutes: 315, TotalActivityPendingMinutes: 135,
		// On-site: approved 90 + 30 = 120; pending 45 + 30 = 75 (the rejected on-site 120 is left out).
		OnSiteApprovedMinutes: 120, OnSitePendingMinutes: 75,
	}
	if got := f.progress(t, user); got != want {
		t.Errorf("progress = %+v\nwant     %+v", got, want)
	}
}

func TestGetProgressMinutes_ANewUserHasZeroEverywhere(t *testing.T) {
	f := newFixture(t)
	user := f.user(t, "empty@example.test")
	if got := f.progress(t, user); got != (lq.GetProgressMinutesRow{}) {
		t.Errorf("a user with no entries has progress %+v, want all zeros", got)
	}
	// Only rejected entries is also zero.
	f.logged(t, user, "rejected", individualCounseling, 60, true)
	if got := f.progress(t, user); got != (lq.GetProgressMinutesRow{}) {
		t.Errorf("a user with only rejected entries has progress %+v, want all zeros", got)
	}
}

func TestGetProgressMinutes_NeverCountsAnotherUsersEntries(t *testing.T) {
	f := newFixture(t)
	mine, other := f.user(t, "mine@example.test"), f.user(t, "other@example.test")
	f.logged(t, mine, "approved", individualCounseling, 60, true)
	f.seedHandComputed(t, other)
	f.logged(t, other, "approved", individualCounseling, 600, true)

	got := f.progress(t, mine)
	if got.DirectApprovedMinutes != 60 || got.TotalActivityApprovedMinutes != 60 || got.OnSiteApprovedMinutes != 60 ||
		got.TotalActivityPendingMinutes != 0 || got.SupervisionApprovedMinutes != 0 {
		t.Errorf("my progress includes another user's entries: %+v", got)
	}
}

func TestGetProgressMinutes_FollowsAStatusChangeAndCountsTimedAndLegacyEntries(t *testing.T) {
	f := newFixture(t)
	user := f.user(t, "moves@example.test")
	timed := f.entry(t, user, spec{date: "2026-10-02", start: "09:00", end: "10:15", minutes: 75, category: individualCounseling})
	f.logged(t, user, "pending", individualCounseling, 30, false) // legacy, no times

	if got := f.progress(t, user); got.DirectPendingMinutes != 105 || got.DirectApprovedMinutes != 0 {
		t.Fatalf("pending timed 75 + legacy 30: %+v", got)
	}
	f.setStatus(t, timed.ID, "approved")
	if got := f.progress(t, user); got.DirectApprovedMinutes != 75 || got.DirectPendingMinutes != 30 {
		t.Fatalf("after approving the timed entry: %+v", got)
	}
	f.setStatus(t, timed.ID, "rejected")
	if got := f.progress(t, user); got.DirectApprovedMinutes != 0 || got.DirectPendingMinutes != 30 || got.TotalActivityApprovedMinutes != 0 {
		t.Fatalf("after rejecting it: %+v", got)
	}
}

func TestGetProgressMinutes_IsOneStatementThatUsesTheUserStatusIndex(t *testing.T) {
	f := newFixture(t)
	// 40 users with 100 legacy entries each, a third of them rejected, so one user's rows are a small
	// slice of the table and the planner has a real choice (EXPLAIN reviewed, S3-02).
	if _, err := f.conn.Exec(f.ctx, `
		INSERT INTO users (organization_id, email)
		SELECT o.id, 'bulk' || n || '@example.test' FROM organizations o, generate_series(1, 40) n WHERE o.name = 'Default Organization'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `
		INSERT INTO entries (user_id, session_date, duration_minutes, category_id, on_site, status, needs_time)
		SELECT u.id, date '2026-01-01' + (n % 200), 15 * (1 + n % 8), c.id, n % 2 = 0,
		       (ARRAY['approved', 'pending', 'rejected'])[1 + n % 3], true
		FROM users u
		JOIN generate_series(1, 100) n ON true
		JOIN categories c ON c.sort_order = 1 + n % 12
		WHERE u.email LIKE 'bulk%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `VACUUM ANALYZE entries`); err != nil {
		t.Fatal(err)
	}
	var user pgtype.UUID
	if err := f.conn.QueryRow(f.ctx, `SELECT id FROM users WHERE email = 'bulk7@example.test'`).Scan(&user); err != nil {
		t.Fatal(err)
	}

	rows, err := f.conn.Query(f.ctx, "EXPLAIN "+lq.ProgressSQL, user) // nosemgrep: no-string-built-sql
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "entries_user_id_status_idx") {
		t.Errorf("the progress query does not use entries_user_id_status_idx:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on entries") {
		t.Errorf("the progress query scans the whole entries table:\n%s", plan)
	}
	t.Logf("plan:\n%s", plan)
}
