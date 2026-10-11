package dbroles_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

// rdsMaster mimics the RDS master user: CREATEROLE and owner of the database, but not a superuser,
// with rds_iam grantable (on RDS through rds_superuser). Running Ensure as this role catches
// statements that only work for a real superuser, which the container's default user is.
const rdsMaster = "rds_master_test"

// prepare returns a connection to a fresh database, acting as rdsMaster, after running Ensure.
func prepare(t *testing.T) *pgx.Conn {
	t.Helper()
	db := server.NewDatabase(t)
	conn := server.Connect(t, db)
	setup := []string{
		`DO $$ BEGIN
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rds_iam') THEN CREATE ROLE rds_iam; END IF;
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + rdsMaster + `') THEN
		     CREATE ROLE ` + rdsMaster + ` CREATEROLE NOSUPERUSER;
		   END IF;
		 END $$`,
		`GRANT rds_iam TO ` + rdsMaster + ` WITH ADMIN OPTION`,
		`ALTER DATABASE ` + pgx.Identifier{db}.Sanitize() + ` OWNER TO ` + rdsMaster,
		`SET ROLE ` + rdsMaster,
	}
	for _, sql := range setup {
		if _, err := conn.Exec(t.Context(), sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	if err := dbroles.Ensure(t.Context(), conn); err != nil {
		t.Fatalf("Ensure as a non-superuser RDS-like master: %v", err)
	}
	return conn
}

// privileges snapshots role attributes, memberships and grants that Ensure manages.
func privileges(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var snapshot string
	err := conn.QueryRow(t.Context(), `
		SELECT string_agg(line, E'\n' ORDER BY line) FROM (
		  SELECT r.rolname || ' login=' || r.rolcanlogin || ' super=' || r.rolsuper || ' createdb=' || r.rolcreatedb AS line
		    FROM pg_roles r WHERE r.rolname IN ('app_rw', 'migrator', 'presignup_ro')
		  UNION ALL
		  SELECT m.rolname || ' member_of ' || g.rolname FROM pg_auth_members a
		    JOIN pg_roles m ON m.oid = a.member JOIN pg_roles g ON g.oid = a.roleid
		   WHERE m.rolname IN ('app_rw', 'migrator', 'presignup_ro')
		  UNION ALL
		  SELECT 'db ' || coalesce(datacl::text, '') FROM pg_database WHERE datname = current_database()
		  UNION ALL
		  SELECT 'schema ' || coalesce(nspacl::text, '') FROM pg_namespace WHERE nspname = 'public'
		  UNION ALL
		  SELECT 'default ' || defaclobjtype::text || ' ' || defaclacl::text FROM pg_default_acl
		) s`).Scan(&snapshot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snapshot
}

func TestEnsure_IsIdempotent(t *testing.T) {
	conn := prepare(t)
	before := privileges(t, conn)
	if err := dbroles.Ensure(t.Context(), conn); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if after := privileges(t, conn); after != before {
		t.Errorf("re-running changed privileges:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestEnsure_RolesLogInThroughIAMWithoutSuperpowers(t *testing.T) {
	conn := prepare(t)
	snapshot := privileges(t, conn)
	for _, want := range []string{
		"app_rw login=true super=false createdb=false",
		"migrator login=true super=false createdb=false",
		"presignup_ro login=true super=false createdb=false",
		"app_rw member_of rds_iam",
		"migrator member_of rds_iam",
		"presignup_ro member_of rds_iam",
	} {
		if !strings.Contains(snapshot, want) {
			t.Errorf("missing %q in:\n%s", want, snapshot)
		}
	}
}

func TestEnsure_MasterKeepsNoMembershipInMigrator(t *testing.T) {
	conn := prepare(t)
	var member bool
	if err := conn.QueryRow(t.Context(), `SELECT pg_has_role(current_user, 'migrator', 'USAGE')`).Scan(&member); err != nil {
		t.Fatalf("query: %v", err)
	}
	if member {
		t.Error("the master still has the privileges of migrator after Ensure; the temporary grant must be revoked")
	}
}

// asRole runs sql as role inside a transaction that is always rolled back.
func asRole(t *testing.T, conn *pgx.Conn, role, sql string) error {
	t.Helper()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatalf("set role: %v", err)
	}
	_, err = tx.Exec(t.Context(), sql)
	return err
}

// isPermissionDenied matches SQLSTATE 42501 (insufficient privilege), which PostgreSQL also raises
// for "must be owner of table" on ALTER and DROP.
func isPermissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func TestEnsure_AppRoleCannotCreateTables(t *testing.T) {
	conn := prepare(t)
	if err := asRole(t, conn, dbroles.AppRole, "CREATE TABLE intruder (id int)"); !isPermissionDenied(err) {
		t.Errorf("app_rw CREATE TABLE: err = %v, want permission denied (SEC-23)", err)
	}
}

func TestEnsure_MigratorDDLAndAppDMLOnMigratorTables(t *testing.T) {
	conn := prepare(t)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	steps := []string{
		"SET LOCAL ROLE migrator",
		"CREATE EXTENSION IF NOT EXISTS citext",
		"CREATE TABLE clients_probe (id bigserial PRIMARY KEY, name citext NOT NULL)",
		"SET LOCAL ROLE app_rw",
		"INSERT INTO clients_probe (name) VALUES ('Pseudonym-A')",
		"UPDATE clients_probe SET name = 'Pseudonym-B'",
		"SELECT count(*) FROM clients_probe",
		"DELETE FROM clients_probe",
	}
	for _, s := range steps {
		if _, err := tx.Exec(t.Context(), s); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	for _, ddl := range []string{"DROP TABLE clients_probe", "ALTER TABLE clients_probe ADD COLUMN x int", "TRUNCATE clients_probe"} {
		if _, err := tx.Exec(t.Context(), "SAVEPOINT s"); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		if _, err := tx.Exec(t.Context(), ddl); !isPermissionDenied(err) {
			t.Errorf("app_rw %q: err = %v, want a permission error", ddl, err)
		}
		if _, err := tx.Exec(t.Context(), "ROLLBACK TO SAVEPOINT s"); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
}
