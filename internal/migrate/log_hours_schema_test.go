package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// S2-01 constraint tests: every rule of the log-hours schema is rejected by the database itself,
// whatever the service layer does (ARC-21). Fixtures are fictional.

// SQLSTATE classes the schema is expected to raise.
const (
	checkViolation     = "23514"
	exclusionViolation = "23P01"
	uniqueViolation    = "23505"
	foreignKeyViolated = "23503"
)

// entryFixture is one INSERT into entries. Nil times model a legacy entry without times.
type entryFixture struct {
	userID    string
	date      string
	start     *string
	end       *string
	minutes   int
	status    string
	needsTime *bool // nil derives it from start, as the application will
	notes     string
}

func clock(s string) *string { return &s }

func timed(userID, date, start, end string, minutes int) entryFixture {
	return entryFixture{userID: userID, date: date, start: clock(start), end: clock(end), minutes: minutes, status: "pending"}
}

func legacy(userID, date string, minutes int) entryFixture {
	return entryFixture{userID: userID, date: date, minutes: minutes, status: "pending"}
}

// rowQuerier is satisfied by the pool and by a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func insertEntry(t *testing.T, pool rowQuerier, e entryFixture) (string, error) {
	t.Helper()
	needsTime := e.start == nil
	if e.needsTime != nil {
		needsTime = *e.needsTime
	}
	var id string
	err := pool.QueryRow(t.Context(), `
		INSERT INTO entries (user_id, session_date, start_time, end_time, duration_minutes, category_id, status, needs_time, notes)
		SELECT $1::uuid, $2::date, $3::time, $4::time, $5, c.id, $6, $7, NULLIF($8, '')
		FROM categories c WHERE c.sort_order = 1
		RETURNING id::text`,
		e.userID, e.date, e.start, e.end, e.minutes, e.status, needsTime, e.notes).Scan(&id)
	return id, err
}

func mustInsertEntry(t *testing.T, pool *pgxpool.Pool, e entryFixture) string {
	t.Helper()
	id, err := insertEntry(t, pool, e)
	if err != nil {
		t.Fatalf("insert %+v: %v", e, err)
	}
	return id
}

func insertUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(),
		`INSERT INTO users (organization_id, email) SELECT id, $1 FROM organizations WHERE name = 'Default Organization' RETURNING id::text`,
		email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func insertClient(t *testing.T, pool *pgxpool.Pool, userID, name string) (string, error) {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(), `INSERT INTO clients (user_id, name) VALUES ($1, $2) RETURNING id::text`, userID, name).Scan(&id)
	return id, err
}

// wantViolation fails unless err is a PostgreSQL error with this SQLSTATE and constraint (or index) name.
func wantViolation(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want a database error %s on %s", err, code, constraint)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("got SQLSTATE %s on %q (%s), want %s on %q", pgErr.Code, pgErr.ConstraintName, pgErr.Message, code, constraint)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestLogHoursSchema_SeedCategoriesMatchClaudeMdSection8(t *testing.T) {
	pool := seedPool(t)
	rows, err := pool.Query(t.Context(), `SELECT name, category_group, sort_order FROM categories ORDER BY sort_order`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	want := []struct{ name, group string }{
		{"Individual Counseling", "direct"},
		{"Group Facilitation", "direct"},
		{"Intake Interviews", "direct"},
		{"Couples / Family Therapy", "direct"},
		{"Psychoeducation", "direct"},
		{"Writing Progress Notes / Documentation", "indirect"},
		{"Staff Meetings", "indirect"},
		{"Case Management", "indirect"},
		{"Assessment Scoring", "indirect"},
		{"Professional Development", "indirect"},
		{"Group Supervision", "supervision"},
		{"Individual Supervision", "supervision"},
	}
	i := 0
	for rows.Next() {
		var name, group string
		var order int
		if err := rows.Scan(&name, &group, &order); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) {
			t.Fatalf("extra category %q", name)
		}
		if name != want[i].name || group != want[i].group || order != i+1 {
			t.Errorf("category %d = (%q, %q, %d), want (%q, %q, %d)", i+1, name, group, order, want[i].name, want[i].group, i+1)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(want) {
		t.Errorf("%d categories seeded, want %d", i, len(want))
	}
}

func TestLogHoursSchema_CategoryRules(t *testing.T) {
	pool := seedPool(t)
	_, err := pool.Exec(t.Context(), `INSERT INTO categories (name, category_group, sort_order) VALUES ('Staff Meetings', 'indirect', 99)`)
	wantViolation(t, err, uniqueViolation, "categories_name_key")
	_, err = pool.Exec(t.Context(), `INSERT INTO categories (name, category_group, sort_order) VALUES ('Fictional', 'direct', 1)`)
	wantViolation(t, err, uniqueViolation, "categories_sort_order_key")
	_, err = pool.Exec(t.Context(), `INSERT INTO categories (name, category_group, sort_order) VALUES ('Fictional', 'other', 99)`)
	wantViolation(t, err, checkViolation, "categories_category_group_check")
}

func TestLogHoursSchema_ValidEntriesAreAccepted(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "valid@example.test")

	mustInsertEntry(t, pool, timed(user, "2026-03-02", "09:00", "10:15", 75))
	mustInsertEntry(t, pool, timed(user, "2026-03-02", "23:00", "24:00", 60)) // ends at midnight
	mustInsertEntry(t, pool, legacy(user, "2026-03-02", 20))                  // legacy: any minutes, flagged "Needs time"

	var needs, status string
	if err := pool.QueryRow(t.Context(), `SELECT needs_time::text, status FROM entries WHERE start_time IS NULL`).Scan(&needs, &status); err != nil {
		t.Fatal(err)
	}
	if needs != "true" || status != "pending" {
		t.Errorf("legacy entry = needs_time %s, status %s", needs, status)
	}
}

func TestLogHoursSchema_EntryCheckConstraints(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "checks@example.test")
	yes, no := true, false

	// Some rules imply others (an end before the start also gives a non-positive duration), and
	// PostgreSQL reports the first failing check by name. A case lists those shadowing checks in
	// without; they are dropped inside a rolled-back transaction so the rule itself is proven to
	// reject the row on its own.
	cases := []struct {
		name       string
		entry      entryFixture
		constraint string
		without    []string
	}{
		{"end equals start", timed(user, "2026-03-02", "09:00", "09:00", 60), "entries_end_after_start_chk", []string{"entries_duration_matches_times_chk", "entries_duration_range_chk"}},
		{"end before start", timed(user, "2026-03-02", "10:00", "09:00", 60), "entries_end_after_start_chk", []string{"entries_duration_matches_times_chk", "entries_duration_range_chk"}},
		{"start without end", entryFixture{userID: user, date: "2026-03-02", start: clock("09:00"), minutes: 60, status: "pending", needsTime: &no}, "entries_times_together_chk", nil},
		{"end without start", entryFixture{userID: user, date: "2026-03-02", end: clock("10:00"), minutes: 60, status: "pending", needsTime: &yes}, "entries_times_together_chk", nil},
		{"start not on a quarter hour", timed(user, "2026-03-02", "09:10", "10:00", 50), "entries_quarter_hour_chk", nil},
		{"end not on a quarter hour", timed(user, "2026-03-02", "09:00", "10:10", 70), "entries_quarter_hour_chk", nil},
		{"start with seconds", timed(user, "2026-03-02", "09:00:30", "10:00", 60), "entries_quarter_hour_chk", []string{"entries_duration_matches_times_chk"}},
		{"duration shorter than the times", timed(user, "2026-03-02", "09:00", "10:00", 45), "entries_duration_matches_times_chk", nil},
		{"duration longer than the times", timed(user, "2026-03-02", "09:00", "10:00", 75), "entries_duration_matches_times_chk", nil},
		{"zero minutes (legacy)", legacy(user, "2026-03-02", 0), "entries_duration_range_chk", nil},
		{"more than a day (legacy)", legacy(user, "2026-03-02", 1441), "entries_duration_range_chk", nil},
		{"timed entry flagged needs_time", entryFixture{userID: user, date: "2026-03-02", start: clock("09:00"), end: clock("10:00"), minutes: 60, status: "pending", needsTime: &yes}, "entries_needs_time_chk", nil},
		{"legacy entry not flagged needs_time", entryFixture{userID: user, date: "2026-03-02", minutes: 60, status: "pending", needsTime: &no}, "entries_needs_time_chk", nil},
		{"unknown status", entryFixture{userID: user, date: "2026-03-02", start: clock("09:00"), end: clock("10:00"), minutes: 60, status: "archived"}, "entries_status_check", nil},
		{"notes over 2000 characters", entryFixture{userID: user, date: "2026-03-02", start: clock("09:00"), end: clock("10:00"), minutes: 60, status: "pending", notes: strings.Repeat("n", 2001)}, "entries_notes_check", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.without) == 0 {
				_, err := insertEntry(t, pool, tc.entry)
				wantViolation(t, err, checkViolation, tc.constraint)
				return
			}
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			for _, shadow := range tc.without {
				// Test-only DDL on a fixed constraint name from the table above (SEC-22 is about request data).
				// nosemgrep: no-string-built-sql
				if _, err := tx.Exec(t.Context(), "ALTER TABLE entries DROP CONSTRAINT "+pgx.Identifier{shadow}.Sanitize()); err != nil {
					t.Fatal(err)
				}
			}
			_, err = insertEntry(t, tx, tc.entry)
			wantViolation(t, err, checkViolation, tc.constraint)
		})
	}
	if n := count(t, pool, `SELECT count(*) FROM entries`); n != 0 {
		t.Errorf("%d rows were stored by rejected inserts", n)
	}

	t.Run("notes of exactly 2000 characters are accepted", func(t *testing.T) {
		e := timed(user, "2026-03-02", "09:00", "10:00", 60)
		e.notes = strings.Repeat("n", 2000)
		mustInsertEntry(t, pool, e)
	})
}

func TestLogHoursSchema_OverlapExclusion(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "overlap-a@example.test")
	other := insertUser(t, pool, "overlap-b@example.test")
	base := mustInsertEntry(t, pool, timed(user, "2026-03-02", "10:00", "11:00", 60))

	t.Run("overlapping inserts are rejected", func(t *testing.T) {
		for name, e := range map[string]entryFixture{
			"identical":         timed(user, "2026-03-02", "10:00", "11:00", 60),
			"starts inside":     timed(user, "2026-03-02", "10:45", "11:30", 45),
			"ends inside":       timed(user, "2026-03-02", "09:30", "10:15", 45),
			"contains":          timed(user, "2026-03-02", "09:00", "12:00", 180),
			"contained":         timed(user, "2026-03-02", "10:15", "10:30", 15),
			"approved overlaps": {userID: user, date: "2026-03-02", start: clock("10:30"), end: clock("11:30"), minutes: 60, status: "approved"},
		} {
			_, err := insertEntry(t, pool, e)
			if err == nil {
				t.Errorf("%s: overlapping insert was accepted", name)
				continue
			}
			wantViolation(t, err, exclusionViolation, "entries_no_overlap_excl")
		}
	})

	t.Run("sessions that only touch, or sit elsewhere, are accepted", func(t *testing.T) {
		mustInsertEntry(t, pool, timed(user, "2026-03-02", "09:00", "10:00", 60))  // ends when the base starts
		mustInsertEntry(t, pool, timed(user, "2026-03-02", "11:00", "12:00", 60))  // starts when the base ends
		mustInsertEntry(t, pool, timed(user, "2026-03-03", "10:00", "11:00", 60))  // same hours, next day
		mustInsertEntry(t, pool, timed(other, "2026-03-02", "10:00", "11:00", 60)) // same hours, another user
	})

	t.Run("a rejected entry does not block", func(t *testing.T) {
		rejected := timed(user, "2026-03-04", "10:00", "11:00", 60)
		rejected.status = "rejected"
		mustInsertEntry(t, pool, rejected)
		mustInsertEntry(t, pool, timed(user, "2026-03-04", "10:00", "11:00", 60))
		// A rejected entry is not blocked by a live one either.
		rejected.date = "2026-03-02"
		mustInsertEntry(t, pool, rejected)
	})

	t.Run("a legacy entry without times does not block", func(t *testing.T) {
		mustInsertEntry(t, pool, legacy(user, "2026-03-05", 60))
		mustInsertEntry(t, pool, legacy(user, "2026-03-05", 60))
		mustInsertEntry(t, pool, timed(user, "2026-03-05", "10:00", "11:00", 60))
	})

	t.Run("moving an entry onto another is rejected", func(t *testing.T) {
		moved := mustInsertEntry(t, pool, timed(user, "2026-03-06", "13:00", "14:00", 60))
		_, err := pool.Exec(t.Context(), `UPDATE entries SET session_date = '2026-03-02', start_time = '10:30', end_time = '11:30' WHERE id = $1::uuid`, moved)
		wantViolation(t, err, exclusionViolation, "entries_no_overlap_excl")
	})

	t.Run("reopening a rejected entry that now collides is rejected", func(t *testing.T) {
		rejected := timed(user, "2026-03-07", "10:00", "11:00", 60)
		rejected.status = "rejected"
		id := mustInsertEntry(t, pool, rejected)
		mustInsertEntry(t, pool, timed(user, "2026-03-07", "10:30", "11:30", 60))
		_, err := pool.Exec(t.Context(), `UPDATE entries SET status = 'pending' WHERE id = $1::uuid`, id)
		wantViolation(t, err, exclusionViolation, "entries_no_overlap_excl")
	})

	t.Run("rejecting an entry frees its slot", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), `UPDATE entries SET status = 'rejected' WHERE id = $1::uuid`, base); err != nil {
			t.Fatal(err)
		}
		mustInsertEntry(t, pool, timed(user, "2026-03-02", "10:15", "10:45", 30))
	})
}

func TestLogHoursSchema_OverlapExclusionHoldsUnderConcurrency(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "race@example.test")

	const writers = 8
	results := make(chan error, writers)
	for range writers {
		go func() {
			_, err := insertEntry(t, pool, timed(user, "2026-03-09", "10:00", "11:00", 60))
			results <- err
		}()
	}
	accepted := 0
	for range writers {
		if err := <-results; err == nil {
			accepted++
		} else {
			wantViolation(t, err, exclusionViolation, "entries_no_overlap_excl")
		}
	}
	if accepted != 1 {
		t.Errorf("%d concurrent identical inserts were accepted, want exactly 1", accepted)
	}
}

func TestLogHoursSchema_ClientRules(t *testing.T) {
	pool := seedPool(t)
	alice := insertUser(t, pool, "clients-a@example.test")
	bob := insertUser(t, pool, "clients-b@example.test")

	if _, err := insertClient(t, pool, alice, "Group A"); err != nil {
		t.Fatal(err)
	}
	// Unique per user, ignoring case.
	_, err := insertClient(t, pool, alice, "GROUP a")
	wantViolation(t, err, uniqueViolation, "clients_user_id_lower_name_idx")
	// The same name is fine for another user.
	if _, err := insertClient(t, pool, bob, "Group A"); err != nil {
		t.Fatal(err)
	}
	_, err = insertClient(t, pool, alice, "   ")
	wantViolation(t, err, checkViolation, "clients_name_check")
	_, err = insertClient(t, pool, alice, strings.Repeat("c", 65))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22001" { // string_data_right_truncation
		t.Errorf("65-character client name err = %v, want SQLSTATE 22001", err)
	}
}

func TestLogHoursSchema_EntryClientsStayWithinOneOwner(t *testing.T) {
	pool := seedPool(t)
	alice := insertUser(t, pool, "links-a@example.test")
	bob := insertUser(t, pool, "links-b@example.test")
	aliceClient, _ := insertClient(t, pool, alice, "Client One")
	aliceSecond, _ := insertClient(t, pool, alice, "Client Two")
	bobClient, _ := insertClient(t, pool, bob, "Client One")
	entry := mustInsertEntry(t, pool, timed(alice, "2026-03-02", "09:00", "10:00", 60))

	link := func(owner, entryID, clientID string) error {
		_, err := pool.Exec(t.Context(), `INSERT INTO entry_clients (entry_id, client_id, user_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, entryID, clientID, owner)
		return err
	}

	if err := link(alice, entry, aliceClient); err != nil {
		t.Fatalf("own client: %v", err)
	}
	if err := link(alice, entry, aliceSecond); err != nil {
		t.Fatalf("several clients on one entry: %v", err)
	}
	wantViolation(t, link(alice, entry, aliceClient), uniqueViolation, "entry_clients_pkey")
	// Another user's client cannot be attached, whichever owner the row claims.
	wantViolation(t, link(alice, entry, bobClient), foreignKeyViolated, "entry_clients_client_id_user_id_fkey")
	wantViolation(t, link(bob, entry, bobClient), foreignKeyViolated, "entry_clients_entry_id_user_id_fkey")

	// Deleting a client removes only its links; deleting the entry removes the rest.
	if _, err := pool.Exec(t.Context(), `DELETE FROM clients WHERE id = $1::uuid`, aliceSecond); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM entry_clients WHERE entry_id = $1::uuid`, entry); n != 1 {
		t.Errorf("links after deleting a client = %d, want 1", n)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM entries WHERE id = $1::uuid`, entry); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM entry_clients`); n != 0 {
		t.Errorf("links after deleting the entry = %d, want 0", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM clients WHERE user_id = $1::uuid`, alice); n != 1 {
		t.Errorf("clients survive an entry delete = %d, want 1", n)
	}
}

func TestLogHoursSchema_EntryReferencesAreEnforced(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "refs@example.test")
	missing := "00000000-0000-4000-8000-000000000000"

	_, err := pool.Exec(t.Context(), `
		INSERT INTO entries (user_id, session_date, start_time, end_time, duration_minutes, category_id)
		VALUES ($1::uuid, '2026-03-02', '09:00', '10:00', 60, $2::uuid)`, user, missing)
	wantViolation(t, err, foreignKeyViolated, "entries_category_id_fkey")

	entry := mustInsertEntry(t, pool, timed(user, "2026-03-02", "09:00", "10:00", 60))
	_, err = pool.Exec(t.Context(), `DELETE FROM categories WHERE sort_order = 1`)
	wantViolation(t, err, foreignKeyViolated, "entries_category_id_fkey")

	// Permanently deleting a user removes their entries and clients (CLAUDE.md §5.1).
	client, _ := insertClient(t, pool, user, "Client One")
	if _, err := pool.Exec(t.Context(), `INSERT INTO entry_clients (entry_id, client_id, user_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, entry, client, user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"entries", "clients", "entry_clients"} {
		if n := count(t, pool, `SELECT count(*) FROM `+table); n != 0 { // nosemgrep: no-string-built-sql
			t.Errorf("%s rows after deleting the user = %d, want 0", table, n)
		}
	}
}

func TestLogHoursSchema_ScheduleIndexExists(t *testing.T) {
	pool := seedPool(t)
	var def string
	err := pool.QueryRow(t.Context(),
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'entries' AND indexname = 'entries_user_id_session_date_start_time_idx'`).Scan(&def)
	if err != nil {
		t.Fatalf("schedule index: %v", err)
	}
	if !strings.Contains(def, "(user_id, session_date, start_time)") {
		t.Errorf("index definition = %s", def)
	}
}
