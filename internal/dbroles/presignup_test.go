package dbroles_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

func TestPreSignUpAdmission_UsesOnlyReadOnlyRoleAndLifecycleSnapshot(t *testing.T) {
	conn := prepare(t)
	applyIdentityAsMigrator(t, conn)
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		email, status        string
		expired, deactivated bool
	}{
		{"pending@example.test", "pending", false, false},
		{"expired@example.test", "pending", true, false},
		{"accepted@example.test", "accepted", false, false},
		{"revoked@example.test", "revoked", false, false},
		{"deactivated@example.test", "pending", false, true},
		{"boundary@example.test", "pending", true, false},
	} {
		if _, err := conn.Exec(t.Context(), "SET ROLE migrator"); err != nil {
			t.Fatal(err)
		}
		expires := at.Add(time.Hour)
		if tc.expired {
			expires = at
		}
		if _, err := conn.Exec(t.Context(), `INSERT INTO invitations(email, organization_id, status, created_at, expires_at, accepted_at)
   SELECT $1, id, $2, $3, $4, CASE WHEN $2 = 'accepted' THEN $3::timestamptz ELSE NULL END FROM organizations WHERE name = 'Default Organization'`, tc.email, tc.status, at.Add(-time.Hour), expires); err != nil {
			t.Fatal(err)
		}
		if tc.deactivated {
			if _, err := conn.Exec(t.Context(), `INSERT INTO users(email, organization_id, status, deactivated_at)
    SELECT $1, id, 'deactivated', $2 FROM organizations WHERE name = 'Default Organization'`, tc.email, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := conn.Exec(t.Context(), "RESET ROLE; SET ROLE presignup_ro"); err != nil {
		t.Fatal(err)
	}
	reader := db.NewPreSignUpReader(conn)
	for _, tc := range []struct {
		email                string
		invited, deactivated bool
	}{
		{"PENDING@EXAMPLE.TEST", true, false}, {"expired@example.test", false, false},
		{"accepted@example.test", false, false}, {"revoked@example.test", false, false},
		{"deactivated@example.test", true, true}, {"boundary@example.test", false, false},
		{"uninvited@example.test", false, false}, {"' OR 1=1 --", false, false}, {"'; DROP TABLE users; --", false, false},
	} {
		result, err := reader.Admission(t.Context(), tc.email, at)
		if err != nil || result.Invited != tc.invited || result.Deactivated != tc.deactivated {
			t.Fatalf("admission %+v: %+v, %v", tc, result, err)
		}
	}
	var count int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM invitations WHERE status = 'pending'").Scan(&count); err != nil || count != 4 {
		t.Fatal("read gate consumed an invitation")
	}
}

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
