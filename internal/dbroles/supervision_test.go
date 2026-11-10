package dbroles_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

// S6-01: the app role runs the supervision and notification queries, and the organization trigger
// locks user rows (FOR SHARE), which needs more than SELECT. These tests prove the role can do all
// of it with only the default privileges, and that the other roles can touch none of it.

func applySupervisionAsMigrator(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), "RESET ROLE; SET ROLE migrator"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00001_baseline.sql", "00002_identity.sql", "00008_supervision_notifications.sql"} {
		sql, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(t.Context(), string(sql)); err != nil {
			t.Fatalf("apply %s as migrator: %v", name, err)
		}
	}
	if _, err := conn.Exec(t.Context(), "RESET ROLE; SET ROLE "+rdsMaster); err != nil {
		t.Fatal(err)
	}
}

func TestSupervision_AppRoleRunsTheLifecycleAndTheOrganizationTrigger(t *testing.T) {
	conn := prepare(t)
	applySupervisionAsMigrator(t, conn)

	// One transaction, because asRole rolls back after every call and the rows depend on each other.
	const lifecycle = `
		INSERT INTO users (id, organization_id, email) SELECT '11111111-1111-4111-8111-111111111111', id, 'trainee@example.test' FROM organizations;
		INSERT INTO users (id, organization_id, email, is_supervisor) SELECT '22222222-2222-4222-8222-222222222222', id, 'supervisor@example.test', true FROM organizations;
		-- The trigger takes FOR SHARE locks on both users as app_rw.
		INSERT INTO supervision_links (id, supervisee_id, supervisor_id, organization_id)
		  SELECT '33333333-3333-4333-8333-333333333333', '11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222', id FROM organizations;
		UPDATE supervision_links SET status = 'accepted', responded_at = now() WHERE id = '33333333-3333-4333-8333-333333333333';
		UPDATE supervision_links SET status = 'ended', ended_at = now() WHERE id = '33333333-3333-4333-8333-333333333333';
		SELECT * FROM supervision_links;
		INSERT INTO notifications (user_id, type) VALUES ('22222222-2222-4222-8222-222222222222', 'supervision.request_received');
		UPDATE notifications SET read_at = now();
		SELECT count(*) FROM notifications WHERE read_at IS NULL;
		DELETE FROM notifications;
		DELETE FROM supervision_links;`
	if err := asRole(t, conn, dbroles.AppRole, lifecycle); err != nil {
		t.Errorf("app_rw supervision lifecycle: %v, want it allowed", err)
	}
	// Unlike the audit log, these tables are not append-only; but the schema stays the migrator's.
	for _, sql := range []string{
		"ALTER TABLE supervision_links DROP CONSTRAINT supervision_links_not_self_chk",
		"DROP TABLE notifications",
		"ALTER TABLE supervision_links DISABLE TRIGGER supervision_links_organization_trg",
	} {
		if err := asRole(t, conn, dbroles.AppRole, sql); err == nil {
			t.Errorf("app_rw %q succeeded, want it refused", sql)
		}
	}
	for _, table := range []string{"supervision_links", "notifications"} {
		if err := asRole(t, conn, dbroles.PreSignUpRole, "SELECT * FROM "+table); !isPermissionDenied(err) { // nosemgrep: no-string-built-sql
			t.Errorf("%s SELECT %s: err = %v, want permission denied", dbroles.PreSignUpRole, table, err)
		}
	}
}
