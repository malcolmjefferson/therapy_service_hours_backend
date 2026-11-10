-- S6-01: supervision_links and notifications (CLAUDE.md §6.2, §6.3, §9; FR-SUP-02, FR-NOT-01).
-- Additive and forward-only (ADR-0003).
--
-- The rules that keep supervision safe live in the database, not only in the service (ARC-21):
-- one live link per supervisee, no self-link, and a supervisee and supervisor who both belong to
-- the link's organization whenever a link is pending or accepted.

-- +goose Up
CREATE TABLE supervision_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- A link is meaningless without either person, so deleting a user (always after deactivation,
    -- CLAUDE.md §5.1) removes the links they were part of.
    supervisee_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    supervisor_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The one organization both people must belong to while the link is pending or accepted.
    -- No cascade: an organization with members cannot be deleted (CLAUDE.md §6.1).
    organization_id uuid NOT NULL REFERENCES organizations(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined', 'cancelled', 'ended')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    -- When the supervisor accepted or declined. Stays set once the link has ended after an acceptance.
    responded_at timestamptz,
    ended_at timestamptz,
    -- Who ended it (the supervisee, the supervisor or an admin). Cleared when that user is deleted;
    -- the link itself is kept as history.
    ended_by uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Self-requests are blocked (CLAUDE.md §6.2).
    CONSTRAINT supervision_links_not_self_chk CHECK (supervisor_id <> supervisee_id),
    -- A supervisor answers a request, so a response time exists exactly for accepted, declined and
    -- (once accepted) ended links; a pending or cancelled request has none.
    CONSTRAINT supervision_links_responded_at_chk CHECK ((status IN ('accepted', 'declined', 'ended')) = (responded_at IS NOT NULL)),
    -- Only an ended link has an end time or an ender.
    CONSTRAINT supervision_links_ended_at_chk CHECK ((status = 'ended') = (ended_at IS NOT NULL)),
    CONSTRAINT supervision_links_ended_by_chk CHECK (ended_by IS NULL OR status = 'ended'),
    -- Time only moves forward through requested, responded and ended.
    CONSTRAINT supervision_links_responded_after_requested_chk CHECK (responded_at IS NULL OR responded_at >= requested_at),
    CONSTRAINT supervision_links_ended_after_responded_chk CHECK (ended_at IS NULL OR ended_at >= responded_at)
);

-- One supervisor per user: at most one pending or accepted link per supervisee (CLAUDE.md §6.2).
-- A declined, cancelled or ended link does not count, so the supervisee can ask someone new.
CREATE UNIQUE INDEX supervision_links_one_live_per_supervisee_idx ON supervision_links (supervisee_id)
    WHERE status IN ('pending', 'accepted');
-- The supervisor's inbox and accepted supervisees, and a supervisee's history, newest first.
CREATE INDEX supervision_links_supervisor_status_idx ON supervision_links (supervisor_id, status, requested_at DESC);
CREATE INDEX supervision_links_supervisee_history_idx ON supervision_links (supervisee_id, requested_at DESC);
-- Foreign-key lookups: ending links when an organization changes or a user is deleted.
CREATE INDEX supervision_links_organization_id_idx ON supervision_links (organization_id);
CREATE INDEX supervision_links_ended_by_idx ON supervision_links (ended_by) WHERE ended_by IS NOT NULL;

-- Both people must belong to the link's organization at the moment a link is pending or accepted
-- (CLAUDE.md §6.2). A CHECK cannot read another table, so a trigger does it. It runs when a link is
-- created, when either person or the organization is changed, and when the status changes, so
-- accepting re-checks (the race-safe accept of S6-05) while declining, cancelling or ending never
-- blocks, which is how an organization change can still close its links (S6-08).
-- FOR SHARE locks both user rows until the link's transaction ends, so an organization change
-- cannot slip in between this check and the commit. A missing user is left to the foreign key.
-- +goose StatementBegin
CREATE FUNCTION supervision_links_check_organization() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    supervisee_org uuid;
    supervisor_org uuid;
BEGIN
    SELECT organization_id INTO supervisee_org FROM users WHERE id = NEW.supervisee_id FOR SHARE;
    IF FOUND AND supervisee_org <> NEW.organization_id THEN
        RAISE EXCEPTION 'supervisee does not belong to the link organization'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'supervision_links_supervisee_organization_chk';
    END IF;
    SELECT organization_id INTO supervisor_org FROM users WHERE id = NEW.supervisor_id FOR SHARE;
    IF FOUND AND supervisor_org <> NEW.organization_id THEN
        RAISE EXCEPTION 'supervisor does not belong to the link organization'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'supervision_links_supervisor_organization_chk';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER supervision_links_organization_trg
    BEFORE INSERT OR UPDATE OF supervisee_id, supervisor_id, organization_id, status ON supervision_links
    FOR EACH ROW
    WHEN (NEW.status IN ('pending', 'accepted'))
    EXECUTE FUNCTION supervision_links_check_organization();

CREATE TABLE notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The recipient. Notifications go with the person.
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Dotted lowercase name such as supervision.request_received; the set of names belongs to the
    -- code that writes them, so a new event type needs no migration.
    type varchar(100) NOT NULL CHECK (type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    -- What the inbox needs to render the event (ids and display names), as a small JSON object.
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Null while unread.
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notifications_payload_chk CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 8192)
);

-- The inbox, newest first, with id breaking ties so pages are stable.
CREATE INDEX notifications_user_created_at_idx ON notifications (user_id, created_at DESC, id DESC);
-- The unread-count poll (about every 60 s per signed-in user) reads only unread rows.
CREATE INDEX notifications_user_unread_idx ON notifications (user_id) WHERE read_at IS NULL;
