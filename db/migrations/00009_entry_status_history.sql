-- S7-01: entry_status_history and the approved-entry immutability backstop
-- (CLAUDE.md §7.1, §9; FR-REV-04, FR-REV-06). Additive and forward-only (ADR-0003).
--
-- The service layer locks approved entries (S7-07); this migration is the database backstop
-- (ARC-21), so a bug or a hand-written statement cannot edit or delete an approved entry either.

-- +goose Up
CREATE TABLE entry_status_history (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- History belongs to the entry: deleting an entry (or its owner) removes it with the entry.
    entry_id uuid NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    from_status text NOT NULL CHECK (from_status IN ('pending', 'approved', 'rejected')),
    to_status text NOT NULL CHECK (to_status IN ('pending', 'approved', 'rejected')),
    -- Who made the change. Cleared when that user is permanently deleted; the snapshot below keeps
    -- the row readable, so the history of someone else's entry survives the reviewer's deletion.
    changed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    changed_by_name varchar(200) NOT NULL CHECK (btrim(changed_by_name) <> ''),
    reason text CHECK (char_length(reason) <= 2000),
    changed_at timestamptz NOT NULL DEFAULT now(),
    -- Only the lifecycle of CLAUDE.md §7.1 is recorded: approve and reject (from pending), revert
    -- (approved to pending) and an edited rejected entry saved again (rejected to pending).
    CONSTRAINT entry_status_history_transition_chk CHECK (
        (from_status, to_status) IN (('pending', 'approved'), ('pending', 'rejected'), ('approved', 'pending'), ('rejected', 'pending'))
    ),
    -- Rejecting and reverting an approval need a reason (CLAUDE.md §7.1).
    CONSTRAINT entry_status_history_reason_chk CHECK (
        to_status = 'approved' OR from_status = 'rejected' OR (reason IS NOT NULL AND btrim(reason) <> '')
    )
);

-- One entry's history, oldest first, with id breaking ties so pages are stable.
CREATE INDEX entry_status_history_entry_changed_at_idx ON entry_status_history (entry_id, changed_at, id);
-- Deleting a reviewer clears this reference; the partial index keeps that lookup from scanning the table.
CREATE INDEX entry_status_history_changed_by_idx ON entry_status_history (changed_by) WHERE changed_by IS NOT NULL;

-- History is append-only (like admin_audit_log): the app role may insert and read, never change or
-- remove a row. Cascades and SET NULL are foreign-key actions, which PostgreSQL performs with the
-- table owner's rights. Default privileges give the app role full DML on new tables, so take it back.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
        REVOKE UPDATE, DELETE ON entry_status_history FROM app_rw;
    END IF;
END $$;
-- +goose StatementEnd

-- Approved entries are immutable (FR-REV-04). The one way out is the revert path (S7-06): in its
-- transaction the service runs
--     SELECT set_config('app.reverting_entry', '<entry id>', true)
-- and then moves that entry from approved to pending, changing only status and the review fields.
-- The setting is transaction-local and names one entry, so it cannot leak to another entry or
-- request. It is a guard against bugs and stray SQL, not against a caller who can run arbitrary
-- statements as the app role; that caller is stopped by authorization, not by this trigger.
--
-- Deleting an approved entry is refused too, except as part of permanently deleting its owner
-- (CLAUDE.md §5.1): by the time the cascade reaches the entry, the user row is already gone.
-- +goose StatementBegin
CREATE FUNCTION entries_protect_approved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- The trigger's WHEN clause means OLD.status is 'approved' here.
    IF TG_OP = 'DELETE' THEN
        IF NOT EXISTS (SELECT 1 FROM users WHERE id = OLD.user_id) THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'an approved entry cannot be deleted'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'entries_approved_immutable_chk';
    END IF;

    IF NEW.status = 'pending'
       AND current_setting('app.reverting_entry', true) = OLD.id::text
       AND (NEW.id, NEW.user_id, NEW.session_date, NEW.start_time, NEW.end_time, NEW.duration_minutes,
            NEW.category_id, NEW.on_site, NEW.notes, NEW.needs_time, NEW.created_at)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.user_id, OLD.session_date, OLD.start_time, OLD.end_time, OLD.duration_minutes,
            OLD.category_id, OLD.on_site, OLD.notes, OLD.needs_time, OLD.created_at) THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'an approved entry cannot be changed; revert the approval first'
        USING ERRCODE = 'check_violation', CONSTRAINT = 'entries_approved_immutable_chk';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER entries_protect_approved_trg
    BEFORE UPDATE OR DELETE ON entries
    FOR EACH ROW
    WHEN (OLD.status = 'approved')
    EXECUTE FUNCTION entries_protect_approved();
