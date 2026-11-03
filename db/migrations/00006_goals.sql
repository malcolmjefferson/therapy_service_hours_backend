-- S3-01: goals (CLAUDE.md §7.2, §7.3, §9; FR-GOAL-01, FR-GOAL-02). Additive and forward-only (ADR-0003).
-- Every user has exactly four goal rows from the moment the users row exists, so a progress read never
-- has to invent a default and a target is always an editable per-user setting, not a constant.

-- +goose Up
CREATE TABLE goals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    goal_type text NOT NULL CHECK (goal_type IN ('direct', 'supervision', 'total_activity', 'on_site')),
    -- Whole hours. NULL only for on_site, which is tracked and never has a target (FR-GOAL-01).
    target_hours integer,
    -- The highest milestone already celebrated, so a milestone is shown once (CLAUDE.md §7.3).
    last_celebrated_percent smallint NOT NULL DEFAULT 0,
    CONSTRAINT goals_user_goal_type_key UNIQUE (user_id, goal_type),
    -- A target, when there is one, is positive.
    CONSTRAINT goals_target_positive_chk CHECK (target_hours IS NULL OR target_hours > 0),
    -- On-site has no target; every other goal has one.
    CONSTRAINT goals_target_presence_chk CHECK ((goal_type = 'on_site') = (target_hours IS NULL)),
    -- Milestones are 25/50/75/100%; 0 means none yet.
    CONSTRAINT goals_celebrated_percent_chk CHECK (last_celebrated_percent IN (0, 25, 50, 75, 100)),
    -- On-site has no milestones to celebrate.
    CONSTRAINT goals_on_site_not_celebrated_chk CHECK (goal_type <> 'on_site' OR last_celebrated_percent = 0)
);

-- The one place the defaults live: Direct Service 1200 h, Supervision 100 h, Total Activity 3000 h, On-site none.
-- Idempotent, so it can back-fill and be re-run without touching a target the user already changed.
-- +goose StatementBegin
CREATE FUNCTION insert_default_goals(p_user_id uuid) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO goals (user_id, goal_type, target_hours)
    VALUES (p_user_id, 'direct', 1200),
           (p_user_id, 'supervision', 100),
           (p_user_id, 'total_activity', 3000),
           (p_user_id, 'on_site', NULL)
    ON CONFLICT (user_id, goal_type) DO NOTHING
$$;
-- +goose StatementEnd

-- A trigger, not application code, so every way a users row is created (the seed, an invited first
-- sign-in, the admin console later) gets its goals in the same transaction (ARC-21).
-- +goose StatementBegin
CREATE FUNCTION users_default_goals() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM insert_default_goals(NEW.id);
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER users_default_goals_trg AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION users_default_goals();

-- Users that exist before this migration (the seeded admin, signed-in users) get their goals too.
SELECT insert_default_goals(id) FROM users;
