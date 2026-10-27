-- S5-01: admin audit log (FR-ADM-07, SEC-51) and the deactivate-then-delete groundwork (FR-ADM-05).
-- Additive and forward-only (ADR-0003).
--
-- The user lifecycle columns already exist: users.status and users.deactivated_at, with the
-- CHECK that keeps them in step, arrived in 00002_identity.sql (S1-04). This migration adds no
-- column to users; it adds the table that records what admins did to them.

-- +goose Up
CREATE TABLE admin_audit_log (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Who acted and who was acted on. Both references are cleared (not blocked, not cascaded) when
    -- that user is permanently deleted, so the row survives; the name snapshots below keep it readable.
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Display name when the action happened (first and last name, or the email when both are empty).
    actor_name varchar(400) NOT NULL CHECK (btrim(actor_name) <> ''),
    -- Dotted lowercase name such as user.deactivate; the set of names belongs to the code that writes them.
    action varchar(100) NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    -- Null for actions on no user (for example creating an organization).
    target_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Display name of the target when the action happened. It is all the log keeps of a deleted person (SEC-44).
    target_name varchar(400) CHECK (target_name IS NULL OR btrim(target_name) <> ''),
    -- What changed, as a small JSON object (for example the old and new role set).
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT admin_audit_log_details_chk CHECK (jsonb_typeof(details) = 'object' AND octet_length(details::text) <= 8192),
    -- A row naming a target user always carries the snapshot; the reverse holds once the user is deleted.
    CONSTRAINT admin_audit_log_target_name_chk CHECK (target_user_id IS NULL OR target_name IS NOT NULL)
);

-- Newest first, with id breaking ties so pages are stable when several rows share a timestamp.
CREATE INDEX admin_audit_log_created_at_idx ON admin_audit_log (created_at DESC, id DESC);
-- Deleting a user clears these references; the partial indexes keep that lookup from scanning the log.
CREATE INDEX admin_audit_log_actor_id_idx ON admin_audit_log (actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX admin_audit_log_target_user_id_idx ON admin_audit_log (target_user_id) WHERE target_user_id IS NOT NULL;

-- The log is append-only (ARC-23, SEC-23, SEC-51): the app role may insert and read, never change
-- or remove a row. Clearing a reference when a user is deleted is a foreign-key action, which
-- PostgreSQL performs with the table owner's rights, so it does not need UPDATE.
-- Default privileges give the app role full DML on new tables; take it back here. The bootstrap
-- repeats the revoke in case it runs after this migration.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
        REVOKE UPDATE, DELETE ON admin_audit_log FROM app_rw;
    END IF;
END $$;
-- +goose StatementEnd
