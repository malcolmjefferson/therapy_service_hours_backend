package migrate_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// S7-01 schema tests: approved entries are immutable except through the revert path, and the status
// history outlives the people in it (FR-REV-04, FR-REV-06). Fixtures are fictional.

const approvedImmutable = "entries_approved_immutable_chk"

func approvedEntry(t *testing.T, pool *pgxpool.Pool, userID, date string) string {
	t.Helper()
	id := mustInsertEntry(t, pool, timed(userID, date, "09:00", "10:00", 60))
	if _, err := pool.Exec(t.Context(), `UPDATE entries SET status = 'approved' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return id
}

func insertHistory(t *testing.T, pool *pgxpool.Pool, entryID, reviewerID, name, from, to, reason string) error {
	t.Helper()
	_, err := pool.Exec(t.Context(), `
		INSERT INTO entry_status_history (entry_id, from_status, to_status, changed_by, changed_by_name, reason)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, $5, NULLIF($6, ''))`,
		entryID, from, to, reviewerID, name, reason)
	return err
}

func TestApprovedEntry_DirectUpdateAndDeleteFail(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-hist@example.test")
	id := approvedEntry(t, pool, owner, "2026-03-02")

	for name, sql := range map[string]string{
		"edit notes":          `UPDATE entries SET notes = 'changed' WHERE id = $1::uuid`,
		"edit on_site":       `UPDATE entries SET on_site = true WHERE id = $1::uuid`,
		"touch updated_at":    `UPDATE entries SET updated_at = now() WHERE id = $1::uuid`,
		"revert without path": `UPDATE entries SET status = 'pending' WHERE id = $1::uuid`,
		"reject":              `UPDATE entries SET status = 'rejected' WHERE id = $1::uuid`,
		"delete":              `DELETE FROM entries WHERE id = $1::uuid`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), sql, id)
			wantViolation(t, err, checkViolation, approvedImmutable)
		})
	}
	if got := count(t, pool, `SELECT count(*) FROM entries WHERE id = $1::uuid AND status = 'approved' AND notes IS NULL`, id); got != 1 {
		t.Fatalf("approved entry changed despite the failures")
	}
}

func TestApprovedEntry_RevertPath(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-revert@example.test")
	id := approvedEntry(t, pool, owner, "2026-03-03")
	other := approvedEntry(t, pool, owner, "2026-03-04")

	revert := func(setting, target string, set string) error {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		if setting != "" {
			if _, err := tx.Exec(t.Context(), `SELECT set_config('app.reverting_entry', $1, true)`, setting); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE entries SET `+set+` WHERE id = $1::uuid`, target); err != nil {
			return err
		}
		return tx.Commit(t.Context())
	}
	const clear = `status = 'pending', reviewed_by = NULL, reviewed_at = NULL, review_note = NULL, updated_at = now()`

	// The setting names one entry: it does not open another.
	wantViolation(t, revert(other, id, clear), checkViolation, approvedImmutable)
	// The revert changes status and review fields only.
	wantViolation(t, revert(id, id, clear+`, notes = 'sneaky'`), checkViolation, approvedImmutable)
	wantViolation(t, revert(id, id, `status = 'rejected'`), checkViolation, approvedImmutable)
	// The setting is transaction-local, so it does not outlive the transaction.
	if _, err := pool.Exec(t.Context(), `UPDATE entries SET status = 'pending' WHERE id = $1::uuid`, id); err == nil {
		t.Fatal("revert succeeded outside the revert path")
	}

	if err := revert(id, id, clear); err != nil {
		t.Fatalf("revert path: %v", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM entries WHERE id = $1::uuid AND status = 'pending'`, id); got != 1 {
		t.Fatal("entry is not pending after the revert")
	}
	// Once pending it is editable and deletable again.
	if _, err := pool.Exec(t.Context(), `UPDATE entries SET notes = 'edited' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("edit after revert: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM entries WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("delete after revert: %v", err)
	}
}

func TestNonApprovedEntriesStayEditable(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-pending@example.test")
	id := mustInsertEntry(t, pool, timed(owner, "2026-03-05", "09:00", "10:00", 60))
	for _, sql := range []string{
		`UPDATE entries SET notes = 'x' WHERE id = $1::uuid`,
		`UPDATE entries SET status = 'approved' WHERE id = $1::uuid`,
	} {
		if _, err := pool.Exec(t.Context(), sql, id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	rejected := mustInsertEntry(t, pool, timed(owner, "2026-03-06", "09:00", "10:00", 60))
	if _, err := pool.Exec(t.Context(), `UPDATE entries SET status = 'rejected' WHERE id = $1::uuid`, rejected); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM entries WHERE id = $1::uuid`, rejected); err != nil {
		t.Fatalf("delete rejected: %v", err)
	}
}

func TestPermanentUserDeletionRemovesApprovedEntriesAndTheirHistory(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-gone@example.test")
	reviewer := insertUser(t, pool, "reviewer-gone@example.test")
	id := approvedEntry(t, pool, owner, "2026-03-07")
	if err := insertHistory(t, pool, id, reviewer, "Rae Reviewer", "pending", "approved", ""); err != nil {
		t.Fatal(err)
	}

	// Permanent delete follows a deactivation (CLAUDE.md §5.1) and takes the person's entries along.
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, owner); err != nil {
		t.Fatalf("deleting a user with approved entries: %v", err)
	}
	if got := count(t, pool, `SELECT count(*) FROM entries WHERE id = $1::uuid`, id); got != 0 {
		t.Fatal("approved entry outlived its owner")
	}
	if got := count(t, pool, `SELECT count(*) FROM entry_status_history WHERE entry_id = $1::uuid`, id); got != 0 {
		t.Fatal("history outlived its entry")
	}
}

func TestHistorySurvivesReviewerDeletion(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-keep@example.test")
	reviewer := insertUser(t, pool, "reviewer-keep@example.test")
	id := approvedEntry(t, pool, owner, "2026-03-08")
	if err := insertHistory(t, pool, id, reviewer, "Rae Reviewer", "pending", "approved", ""); err != nil {
		t.Fatal(err)
	}
	if err := insertHistory(t, pool, id, reviewer, "Rae Reviewer", "approved", "pending", "wrong client"); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, reviewer); err != nil {
		t.Fatalf("deleting a reviewer named in the history: %v", err)
	}
	rows, err := pool.Query(t.Context(), `SELECT changed_by::text, changed_by_name FROM entry_status_history WHERE entry_id = $1::uuid`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var by *string
		var name string
		if err := rows.Scan(&by, &name); err != nil {
			t.Fatal(err)
		}
		if by != nil || name != "Rae Reviewer" {
			t.Errorf("row = (%v, %q), want changed_by cleared and the name kept", by, name)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("history rows = %d, want 2", n)
	}
}

func TestHistoryConstraints(t *testing.T) {
	pool := seedPool(t)
	owner := insertUser(t, pool, "owner-chk@example.test")
	id := mustInsertEntry(t, pool, timed(owner, "2026-03-09", "09:00", "10:00", 60))

	tests := []struct {
		name             string
		from, to, reason string
		who              string
		constraint       string
	}{
		{"same status", "pending", "pending", "why", "Rae", "entry_status_history_transition_chk"},
		{"approved to rejected", "approved", "rejected", "why", "Rae", "entry_status_history_transition_chk"},
		{"rejected to approved", "rejected", "approved", "", "Rae", "entry_status_history_transition_chk"},
		{"unknown status", "pending", "archived", "", "Rae", ""},
		{"reject needs a reason", "pending", "rejected", "", "Rae", "entry_status_history_reason_chk"},
		{"revert needs a reason", "approved", "pending", "", "Rae", "entry_status_history_reason_chk"},
		{"blank name", "pending", "approved", "", "  ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := insertHistory(t, pool, id, "", tc.who, tc.from, tc.to, tc.reason)
			if tc.constraint == "" {
				if err == nil {
					t.Fatal("insert succeeded")
				}
				return
			}
			wantViolation(t, err, checkViolation, tc.constraint)
		})
	}
	for _, ok := range [][3]string{{"pending", "approved", ""}, {"pending", "rejected", "wrong date"}, {"approved", "pending", "wrong client"}, {"rejected", "pending", ""}} {
		if err := insertHistory(t, pool, id, "", "Rae", ok[0], ok[1], ok[2]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	if err := insertHistory(t, pool, "00000000-0000-0000-0000-000000000000", "", "Rae", "pending", "approved", ""); err == nil {
		t.Error("history for a missing entry was accepted")
	}
}
