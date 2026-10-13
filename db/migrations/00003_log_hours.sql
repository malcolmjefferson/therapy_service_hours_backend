-- S2-01: log-hours schema (CLAUDE.md §7.1, §7.4, §8, §9). Additive and forward-only (ADR-0003).
-- Session times are in the first version of the schema, so no later migration has to backfill them.
-- Every rule below is enforced by the database as well as by the service layer (ARC-21).
-- btree_gist comes from 00001_baseline.sql.

-- +goose Up
CREATE TABLE categories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(100) NOT NULL UNIQUE CHECK (btrim(name) <> ''),
    -- CLAUDE.md §9 names this column "group", a reserved word in SQL; it is category_group here.
    category_group text NOT NULL CHECK (category_group IN ('direct', 'indirect', 'supervision')),
    sort_order smallint NOT NULL UNIQUE CHECK (sort_order > 0)
);

-- The 12 categories of CLAUDE.md §8, in display order.
INSERT INTO categories (name, category_group, sort_order) VALUES
    ('Individual Counseling', 'direct', 1),
    ('Group Facilitation', 'direct', 2),
    ('Intake Interviews', 'direct', 3),
    ('Couples / Family Therapy', 'direct', 4),
    ('Psychoeducation', 'direct', 5),
    ('Writing Progress Notes / Documentation', 'indirect', 6),
    ('Staff Meetings', 'indirect', 7),
    ('Case Management', 'indirect', 8),
    ('Assessment Scoring', 'indirect', 9),
    ('Professional Development', 'indirect', 10),
    ('Group Supervision', 'supervision', 11),
    ('Individual Supervision', 'supervision', 12)
ON CONFLICT DO NOTHING;

CREATE TABLE clients (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Pseudonyms or initials only (CLAUDE.md §0.7). 64 characters: SEC-26.
    name varchar(64) NOT NULL CHECK (btrim(name) <> ''),
    -- Lets entry_clients prove that an entry and its client belong to the same user.
    UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX clients_user_id_lower_name_idx ON clients (user_id, lower(name));

CREATE TABLE entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_date date NOT NULL,
    -- Local clock time with no timezone conversion. NULL only for legacy imports (needs_time).
    start_time time,
    end_time time,
    -- Minutes are the stored unit; hours are derived for display and never typed in.
    duration_minutes integer NOT NULL,
    category_id uuid NOT NULL REFERENCES categories(id),
    on_site boolean NOT NULL DEFAULT false,
    notes text CHECK (char_length(notes) <= 2000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    needs_time boolean NOT NULL DEFAULT false,
    reviewed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at timestamptz,
    review_note text CHECK (char_length(review_note) <= 2000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- One user's entry id may also be referenced together with its owner (see entry_clients).
    UNIQUE (id, user_id),

    -- FR-LOG-03: the end is after the start. Both times are on session_date, so a session cannot
    -- cross midnight; a session may end exactly at 24:00.
    CONSTRAINT entries_end_after_start_chk CHECK (end_time > start_time),
    -- Start and end are both set or both null (null only for legacy entries).
    CONSTRAINT entries_times_together_chk CHECK ((start_time IS NULL) = (end_time IS NULL)),
    -- Times move in 15-minute steps (ASM-02): seconds since midnight divide by 900.
    CONSTRAINT entries_quarter_hour_chk CHECK (
        start_time IS NULL
        OR (extract(epoch FROM start_time) % 900 = 0 AND extract(epoch FROM end_time) % 900 = 0)
    ),
    -- duration_minutes agrees with the times when they are present.
    CONSTRAINT entries_duration_matches_times_chk CHECK (
        start_time IS NULL
        OR duration_minutes * 60 = extract(epoch FROM (end_time - start_time))
    ),
    CONSTRAINT entries_duration_range_chk CHECK (duration_minutes BETWEEN 1 AND 1440),
    -- The "Needs time" flag is exactly the legacy case.
    CONSTRAINT entries_needs_time_chk CHECK (needs_time = (start_time IS NULL)),

    -- FR-LOG-04: one user's non-rejected sessions with times never overlap. The range is
    -- half-open, so back-to-back sessions (10:00-11:00 then 11:00-12:00) are allowed.
    CONSTRAINT entries_no_overlap_excl EXCLUDE USING gist (
        user_id WITH =,
        tsrange(session_date + start_time, session_date + end_time) WITH &&
    ) WHERE (status <> 'rejected' AND start_time IS NOT NULL)
);
-- The schedule path (CLAUDE.md §9).
CREATE INDEX entries_user_id_session_date_start_time_idx ON entries (user_id, session_date, start_time);
CREATE INDEX entries_category_id_idx ON entries (category_id);

CREATE TABLE entry_clients (
    entry_id uuid NOT NULL,
    client_id uuid NOT NULL,
    -- Both foreign keys carry the owner, so an entry can only reference its owner's clients.
    user_id uuid NOT NULL,
    PRIMARY KEY (entry_id, client_id),
    FOREIGN KEY (entry_id, user_id) REFERENCES entries (id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (client_id, user_id) REFERENCES clients (id, user_id) ON DELETE CASCADE
);
CREATE INDEX entry_clients_client_id_idx ON entry_clients (client_id);
