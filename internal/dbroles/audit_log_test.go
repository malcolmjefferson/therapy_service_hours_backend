package dbroles_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

// applyAuditLogAsMigrator creates the identity tables and admin_audit_log the way the migrate Lambda
// does: as migrator, so app_rw gets its default privileges.
func applyAuditLogAsMigrator(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), "RESET ROLE; SET ROLE migrator"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00001_baseline.sql", "00002_identity.sql", "00005_admin_audit_log.sql"} {
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

const seedAuditRow = `
	INSERT INTO admin_audit_log (actor_name, action, target_name, details)
	VALUES ('Ada Admin', 'user.deactivate', 'Tara Target', '{}')`

func TestAuditLog_AppRoleCanOnlyInsertAndRead(t *testing.T) {
	conn := prepare(t)
	applyAuditLogAsMigrator(t, conn)

	for _, sql := range []string{seedAuditRow, "SELECT * FROM admin_audit_log"} {
		if err := asRole(t, conn, dbroles.AppRole, sql); err != nil {
			t.Errorf("app_rw %q: %v, want it allowed (ARC-23)", sql, err)
		}
	}
	for _, sql := range []string{
		"UPDATE admin_audit_log SET action = 'user.edit'",
		"DELETE FROM admin_audit_log",
		"TRUNCATE admin_audit_log",
		"ALTER TABLE admin_audit_log ADD COLUMN forged text",
		"DROP TABLE admin_audit_log",
	} {
		if err := asRole(t, conn, dbroles.AppRole, sql); !isPermissionDenied(err) {
			t.Errorf("app_rw %q: err = %v, want permission denied (ARC-23, SEC-51)", sql, err)
		}
	}
	// The other tables keep full DML for the app role; only the audit table is narrowed.
	if err := asRole(t, conn, dbroles.AppRole, "UPDATE organizations SET name = name"); err != nil {
		t.Errorf("app_rw UPDATE organizations: %v", err)
	}
	// Other roles never see the log.
	for _, role := range []string{dbroles.PreSignUpRole} {
		if err := asRole(t, conn, role, "SELECT * FROM admin_audit_log"); !isPermissionDenied(err) {
			t.Errorf("%s SELECT: err = %v, want permission denied", role, err)
		}
	}
}

func TestAuditLog_DeletingAUserStillWorksForTheAppRole(t *testing.T) {
	conn := prepare(t)
	applyAuditLogAsMigrator(t, conn)
	// Clearing the reference is a foreign-key action, which needs no UPDATE right on the log.
	if _, err := conn.Exec(t.Context(), `
		SET ROLE migrator;
		INSERT INTO users (id, organization_id, email, status, deactivated_at)
		  SELECT '11111111-1111-4111-8111-111111111111', id, 'gone@example.test', 'deactivated', now()
		  FROM organizations WHERE name = 'Default Organization';
		INSERT INTO admin_audit_log (target_user_id, actor_name, action, target_name)
		  VALUES ('11111111-1111-4111-8111-111111111111', 'Ada Admin', 'user.delete', 'Tara Target');
		RESET ROLE; SET ROLE `+rdsMaster); err != nil {
		t.Fatal(err)
	}
	if err := asRole(t, conn, dbroles.AppRole, `DELETE FROM users WHERE id = '11111111-1111-4111-8111-111111111111'`); err != nil {
		t.Fatalf("app_rw deleting a user named in the log: %v", err)
	}
}

func TestAuditLog_BootstrapRevokesWhenItRunsAfterTheMigration(t *testing.T) {
	conn := prepare(t)
	applyAuditLogAsMigrator(t, conn)
	// Restore what default privileges alone would have granted, then run the bootstrap again.
	if _, err := conn.Exec(t.Context(), `SET ROLE migrator;
	    GRANT UPDATE, DELETE ON admin_audit_log TO app_rw;
	    RESET ROLE; SET ROLE `+rdsMaster); err != nil {
		t.Fatal(err)
	}
	if err := dbroles.Ensure(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"UPDATE admin_audit_log SET action = 'user.edit'", "DELETE FROM admin_audit_log"} {
		if err := asRole(t, conn, dbroles.AppRole, sql); !isPermissionDenied(err) {
			t.Errorf("app_rw %q after bootstrap: err = %v, want permission denied", sql, err)
		}
	}
}
