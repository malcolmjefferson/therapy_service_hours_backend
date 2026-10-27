// Package dbroles creates the application database roles (ADR-0001, S1-17). It runs once per
// deploy from the bootstrap custom resource, as the RDS master user, and is idempotent: a re-run
// changes nothing.
package dbroles

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Role names. All log in only with RDS IAM tokens (membership in rds_iam).
const (
	// AppRole is used by the api Lambda: DML only, no DDL (ARC-25, SEC-23).
	AppRole = "app_rw"
	// MigratorRole is used by the migrate Lambda: owns the schema objects it creates (DDL).
	MigratorRole = "migrator"
	// PreSignUpRole reads only users and invitations for the Cognito allowlist trigger.
	PreSignUpRole = "presignup_ro"
)

// statements are static SQL; nothing from the request is interpolated (SEC-20). The database
// name for GRANT ... ON DATABASE comes from current_database() inside the server.
var statements = []string{
	`DO $$ BEGIN
	   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN CREATE ROLE app_rw LOGIN; END IF;
	   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'migrator') THEN CREATE ROLE migrator LOGIN; END IF;
	   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'presignup_ro') THEN CREATE ROLE presignup_ro LOGIN; END IF;
	 END $$`,
	// IAM authentication (ADR-0001). rds_iam exists on every RDS PostgreSQL instance.
	`GRANT rds_iam TO app_rw, migrator, presignup_ro`,
	`DO $$ BEGIN
	   EXECUTE format('GRANT CONNECT ON DATABASE %I TO app_rw, migrator, presignup_ro', current_database());
	   -- CREATE on the database lets migrator install trusted extensions (citext, btree_gist).
	   EXECUTE format('GRANT CREATE ON DATABASE %I TO migrator', current_database());
	 END $$`,
	// Nobody else may create objects in public (already the default since PostgreSQL 15).
	`REVOKE CREATE ON SCHEMA public FROM PUBLIC`,
	`GRANT USAGE, CREATE ON SCHEMA public TO migrator`,
	`GRANT USAGE ON SCHEMA public TO app_rw, presignup_ro`,
	// Tables and sequences that migrator creates are readable and writable by app_rw, never
	// alterable or droppable. Append-only audit tables tighten this in their own migration (ARC-23).
	// ALTER DEFAULT PRIVILEGES FOR ROLE needs privileges of that role. The RDS master is not a
	// superuser, and since PostgreSQL 16 creating a role grants only ADMIN OPTION on it, not
	// membership. So the master joins migrator just for these two statements and leaves again,
	// all inside the one transaction.
	`GRANT migrator TO CURRENT_USER WITH INHERIT TRUE, SET FALSE`,
	`ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_rw`,
	`ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO app_rw`,
	// Default SELECT grants would expose every later table, including client data.
	// Migration 00002 grants these exact tables if the role was bootstrapped first;
	// this block handles the reverse order without granting broader privileges.
	`DO $$ BEGIN
	   IF to_regclass('public.users') IS NOT NULL THEN GRANT SELECT ON public.users TO presignup_ro; END IF;
	   IF to_regclass('public.invitations') IS NOT NULL THEN GRANT SELECT ON public.invitations TO presignup_ro; END IF;
	 END $$`,
	// Audit tables are append-only (ARC-23): migration 00005 revokes UPDATE and DELETE from app_rw
	// as it creates admin_audit_log; this covers a bootstrap that runs afterwards.
	`DO $$ BEGIN
	   IF to_regclass('public.admin_audit_log') IS NOT NULL THEN REVOKE UPDATE, DELETE ON public.admin_audit_log FROM app_rw; END IF;
	 END $$`,
	`REVOKE migrator FROM CURRENT_USER`,
}

// Execer runs a statement. *pgxpool.Pool, *pgx.Conn and pgx.Tx satisfy it.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// TxBeginner starts a transaction. *pgxpool.Pool and *pgx.Conn satisfy it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Ensure creates or confirms all roles and their privileges in one transaction.
func Ensure(ctx context.Context, db TxBeginner) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // No-op after a commit.
	for i, stmt := range statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("role bootstrap statement %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
