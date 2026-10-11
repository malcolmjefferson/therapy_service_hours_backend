-- S1-04: identity and invitation lifecycle (ADR-0010). Additive and forward-only.
-- +goose Up
CREATE TABLE organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(200) NOT NULL UNIQUE CHECK (btrim(name) <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    email citext NOT NULL UNIQUE CHECK (length(email::text) BETWEEN 1 AND 254),
    first_name varchar(200) NOT NULL DEFAULT '',
    last_name varchar(200) NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deactivated')),
    cognito_sub varchar(128) UNIQUE CHECK (cognito_sub IS NULL OR btrim(cognito_sub) <> ''),
    is_supervisor boolean NOT NULL DEFAULT false,
    is_admin boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    deactivated_at timestamptz,
    CHECK ((status = 'deactivated') = (deactivated_at IS NOT NULL))
);
CREATE INDEX users_organization_id_idx ON users (organization_id);

CREATE TABLE invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email citext NOT NULL CHECK (length(email::text) BETWEEN 1 AND 254),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    roles text[] NOT NULL DEFAULT '{}' CHECK (roles <@ ARRAY['admin', 'supervisor']::text[]),
    invited_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '14 days'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked')),
    accepted_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK ((status = 'accepted') = (accepted_at IS NOT NULL))
);
CREATE UNIQUE INDEX invitations_pending_email_idx ON invitations (email) WHERE status = 'pending';
CREATE INDEX invitations_organization_id_idx ON invitations (organization_id);

INSERT INTO organizations (name) VALUES ('Default Organization') ON CONFLICT (name) DO NOTHING;

-- Bootstrap may run before or after this migration during staged releases. Grant
-- exactly these two tables if S1-17's role already exists; the bootstrap also
-- grants existing identity tables when introduced after the migration.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'presignup_ro') THEN
        GRANT SELECT ON users, invitations TO presignup_ro;
    END IF;
END $$;
-- +goose StatementEnd
