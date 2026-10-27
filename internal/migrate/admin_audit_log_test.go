package migrate_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// S5-01 schema tests: the audit log outlives the people in it, pages newest first through an
// index, and refuses malformed rows in the database itself (ARC-21). Fixtures are fictional.

// stringDataTooLong is SQLSTATE 22001, raised when a value exceeds its varchar length.
const stringDataTooLong = "22001"

type auditFixture struct {
	actorID, actorName, action, targetID, targetName, details string
}

// insertAudit writes one row. Empty strings model NULL, except details, where empty means the default.
func insertAudit(t *testing.T, pool *pgxpool.Pool, a auditFixture) (string, error) {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(), `
		INSERT INTO admin_audit_log (actor_id, actor_name, action, target_user_id, target_name, details)
		VALUES (NULLIF($1, '')::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, ''), COALESCE(NULLIF($6, '')::jsonb, '{}'::jsonb))
		RETURNING id::text`,
		a.actorID, a.actorName, a.action, a.targetID, a.targetName, a.details).Scan(&id)
	return id, err
}

func mustInsertAudit(t *testing.T, pool *pgxpool.Pool, a auditFixture) string {
	t.Helper()
	id, err := insertAudit(t, pool, a)
	if err != nil {
		t.Fatalf("insert %+v: %v", a, err)
	}
	return id
}

func TestAdminAuditLog_RowsSurviveDeletionOfTargetAndActor(t *testing.T) {
	pool := seedPool(t)
	admin := insertUser(t, pool, "audit-admin@example.test")
	target := insertUser(t, pool, "audit-target@example.test")

	deactivate := mustInsertAudit(t, pool, auditFixture{admin, "Ada Admin", "user.deactivate", target, "Tara Target", `{"reason": "left the programme"}`})
	remove := mustInsertAudit(t, pool, auditFixture{admin, "Ada Admin", "user.delete", target, "Tara Target", ""})

	// Permanent delete follows a deactivation (CLAUDE.md §5.1).
	if _, err := pool.Exec(t.Context(),
		`UPDATE users SET status = 'deactivated', deactivated_at = now() WHERE id = $1::uuid`, target); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, target); err != nil {
		t.Fatalf("deleting a user named in the log: %v", err)
	}
	for _, id := range []string{deactivate, remove} {
		var targetID, targetName *string
		var actorName string
		err := pool.QueryRow(t.Context(),
			`SELECT target_user_id::text, target_name, actor_name FROM admin_audit_log WHERE id = $1::uuid`, id).Scan(&targetID, &targetName, &actorName)
		if err != nil {
			t.Fatalf("row %s after deleting its target: %v", id, err)
		}
		if targetID != nil {
			t.Errorf("target_user_id = %s after the delete, want NULL", *targetID)
		}
		if targetName == nil || *targetName != "Tara Target" {
			t.Errorf("target_name = %v, want the snapshot kept", targetName)
		}
		if actorName != "Ada Admin" {
			t.Errorf("actor_name = %q", actorName)
		}
	}

	// The actor can be deleted too (a later admin removes them); the log keeps their name.
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, admin); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM admin_audit_log WHERE actor_id IS NULL AND actor_name = 'Ada Admin'`); n != 2 {
		t.Errorf("rows kept after the actor was deleted = %d, want 2", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM admin_audit_log`); n != 2 {
		t.Errorf("rows in the log = %d, want 2", n)
	}
}

func TestAdminAuditLog_NewestFirstPagingUsesTheIndex(t *testing.T) {
	pool := seedPool(t)
	admin := insertUser(t, pool, "paging-admin@example.test")
	// Two rows share a timestamp, so the order depends on the id tie-break.
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO admin_audit_log (actor_id, actor_name, action, created_at)
		SELECT $1::uuid, 'Ada Admin', 'org.create', timestamptz '2026-10-01 09:00:00+00' + (n / 2) * interval '1 minute'
		FROM generate_series(0, 599) AS n`, admin); err != nil {
		t.Fatal(err)
	}

	const page = `SELECT id::text, created_at FROM admin_audit_log ORDER BY created_at DESC, id DESC LIMIT 50`
	rows, err := pool.Query(t.Context(), page)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prevAt time.Time
	var prevID string
	n := 0
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			t.Fatal(err)
		}
		if n > 0 && (at.After(prevAt) || (at.Equal(prevAt) && id >= prevID)) {
			t.Fatalf("row %d (%s, %s) is not before (%s, %s)", n, at, id, prevAt, prevID)
		}
		prevAt, prevID = at, id
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("page rows = %d, want 50", n)
	}

	// With the sequential scan off the planner has no choice but to show whether the index can serve the sort.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	for _, stmt := range []string{`ANALYZE admin_audit_log`, `SET LOCAL enable_seqscan = off`, `SET LOCAL enable_sort = off`} {
		if _, err := tx.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := tx.Query(t.Context(), `EXPLAIN `+page) // nosemgrep: no-string-built-sql
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var lines []string
	for plan.Next() {
		var line string
		if err := plan.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if text := strings.Join(lines, "\n"); !strings.Contains(text, "admin_audit_log_created_at_idx") {
		t.Errorf("newest-first page does not use admin_audit_log_created_at_idx:\n%s", text)
	}
}

func TestAdminAuditLog_ConstraintsRejectMalformedRows(t *testing.T) {
	pool := seedPool(t)
	admin := insertUser(t, pool, "constraints-admin@example.test")
	target := insertUser(t, pool, "constraints-target@example.test")
	missing := "00000000-0000-4000-8000-000000000000"
	valid := auditFixture{actorID: admin, actorName: "Ada Admin", action: "user.edit"}

	// Actions on no user are fine, and so is a row whose target is named.
	mustInsertAudit(t, pool, auditFixture{actorID: admin, actorName: "Ada Admin", action: "org.rename", details: `{"from": "A", "to": "B"}`})
	mustInsertAudit(t, pool, auditFixture{admin, "Ada Admin", "user.roles.change", target, "Tara Target", `{"added": ["supervisor"]}`})

	for _, tc := range []struct {
		name       string
		mutate     func(*auditFixture)
		constraint string
		code       string
	}{
		{"blank actor name", func(a *auditFixture) { a.actorName = "   " }, "admin_audit_log_actor_name_check", checkViolation},
		{"upper-case action", func(a *auditFixture) { a.action = "User.Edit" }, "admin_audit_log_action_check", checkViolation},
		{"action with a space", func(a *auditFixture) { a.action = "edit user" }, "admin_audit_log_action_check", checkViolation},
		{"empty action", func(a *auditFixture) { a.action = "" }, "admin_audit_log_action_check", checkViolation},
		{"action with an injection payload", func(a *auditFixture) { a.action = "x'; DROP TABLE users; --" }, "admin_audit_log_action_check", checkViolation},
		{"action over 100 characters", func(a *auditFixture) { a.action = strings.Repeat("a", 101) }, "", stringDataTooLong},
		{"details that is not an object", func(a *auditFixture) { a.details = `["a"]` }, "admin_audit_log_details_chk", checkViolation},
		{"details over 8 KiB", func(a *auditFixture) { a.details = `{"note": "` + strings.Repeat("x", 8200) + `"}` }, "admin_audit_log_details_chk", checkViolation},
		{"target without a name snapshot", func(a *auditFixture) { a.targetID = target }, "admin_audit_log_target_name_chk", checkViolation},
		{"unknown actor", func(a *auditFixture) { a.actorID = missing }, "admin_audit_log_actor_id_fkey", foreignKeyViolated},
		{"unknown target", func(a *auditFixture) { a.targetID, a.targetName = missing, "Nobody" }, "admin_audit_log_target_user_id_fkey", foreignKeyViolated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := valid
			tc.mutate(&row)
			_, err := insertAudit(t, pool, row)
			if tc.constraint == "" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
					t.Fatalf("err = %v, want SQLSTATE %s", err, tc.code)
				}
				return
			}
			wantViolation(t, err, tc.code, tc.constraint)
		})
	}
	if n := count(t, pool, `SELECT count(*) FROM admin_audit_log`); n != 2 {
		t.Errorf("rows in the log = %d, want only the 2 valid ones", n)
	}
}

func TestUserLifecycle_DeactivatedAtMatchesStatus(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "lifecycle@example.test")

	// Deactivation needs its timestamp and reactivation clears it, so a deactivated user is always dated.
	_, err := pool.Exec(t.Context(), `UPDATE users SET status = 'deactivated' WHERE id = $1::uuid`, user)
	wantViolation(t, err, checkViolation, "users_check")
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'deactivated', deactivated_at = now() WHERE id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `UPDATE users SET status = 'active' WHERE id = $1::uuid`, user)
	wantViolation(t, err, checkViolation, "users_check")
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status = 'active', deactivated_at = NULL WHERE id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
}
