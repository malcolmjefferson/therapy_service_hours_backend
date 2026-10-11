package dbroles_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

func applyIdentityAsMigrator(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), "RESET ROLE; SET ROLE migrator"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00001_baseline.sql", "00002_identity.sql"} {
		sql, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(t.Context(), string(sql)); err != nil {
			t.Fatalf("apply %s as migrator: %v", name, err)
		}
	}
	if _, err := conn.Exec(t.Context(), `CREATE TABLE private_client_probe (id int); RESET ROLE; SET ROLE rds_master_test`); err != nil {
		t.Fatal(err)
	}
}

func TestPreSignUpRole_ReadsOnlyIdentityTablesAndCannotWrite(t *testing.T) {
	conn := prepare(t)
	applyIdentityAsMigrator(t, conn)
	for _, sql := range []string{"SELECT * FROM users", "SELECT * FROM invitations"} {
		if err := asRole(t, conn, dbroles.PreSignUpRole, sql); err != nil {
			t.Fatalf("identity read denied: %v", err)
		}
	}
	for _, sql := range []string{
		"SELECT * FROM organizations",
		"SELECT * FROM private_client_probe",
		"INSERT INTO users (email) VALUES ('pseudonym-a@example.test')",
		"UPDATE users SET is_admin = true",
		"DELETE FROM users",
		"INSERT INTO invitations (email) VALUES ('pseudonym-a@example.test')",
		"UPDATE invitations SET status = 'revoked'",
		"DELETE FROM invitations",
		"TRUNCATE users",
		"CREATE TABLE unauthorized_probe (id int)",
	} {
		if err := asRole(t, conn, dbroles.PreSignUpRole, sql); !isPermissionDenied(err) {
			t.Errorf("presignup_ro %q: err = %v, want permission denied", sql, err)
		}
	}
	before := privileges(t, conn)
	if err := dbroles.Ensure(t.Context(), conn); err != nil {
		t.Fatalf("bootstrap after migrations: %v", err)
	}
	if after := privileges(t, conn); after != before {
		t.Error("repeated bootstrap changed role privileges")
	}
}

func TestPreSignUpRole_BootstrapGrantsPreviouslyCreatedIdentityTables(t *testing.T) {
	conn := prepare(t)
	applyIdentityAsMigrator(t, conn)
	if _, err := conn.Exec(t.Context(), `RESET ROLE; SET ROLE migrator;
	    REVOKE SELECT ON users, invitations FROM presignup_ro;
	    RESET ROLE; SET ROLE rds_master_test`); err != nil {
		t.Fatal(err)
	}
	if err := dbroles.Ensure(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"SELECT * FROM users", "SELECT * FROM invitations"} {
		if err := asRole(t, conn, dbroles.PreSignUpRole, sql); err != nil {
			t.Fatalf("bootstrap failed to grant identity read: %v", err)
		}
	}
	if err := asRole(t, conn, dbroles.PreSignUpRole, "SELECT * FROM private_client_probe"); !isPermissionDenied(err) {
		t.Errorf("bootstrap granted unrelated table: %v", err)
	}
}
