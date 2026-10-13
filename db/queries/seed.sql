-- name: LockIdentitySeed :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_id)::bigint);

-- name: EnsureDefaultOrganization :exec
INSERT INTO organizations (name) VALUES ('Default Organization') ON CONFLICT (name) DO NOTHING;

-- name: InsertSeedUser :execrows
INSERT INTO users (organization_id, email, first_name, last_name, is_admin, is_supervisor)
VALUES (sqlc.arg(organization_id), sqlc.arg(email)::citext, sqlc.arg(first_name), sqlc.arg(last_name), sqlc.arg(is_admin), sqlc.arg(is_supervisor))
ON CONFLICT (email) DO NOTHING;

-- name: GetSeedUser :one
SELECT * FROM users WHERE email = sqlc.arg(email)::citext FOR UPDATE;

-- name: GetSeedPendingInvitation :one
SELECT * FROM invitations WHERE email = sqlc.arg(email)::citext AND status = 'pending' FOR UPDATE;

-- name: RevokeExpiredSeedInvitation :execrows
UPDATE invitations SET status = 'revoked'
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at <= sqlc.arg(at)::timestamptz;

-- name: InsertSeedInvitation :execrows
INSERT INTO invitations (organization_id, email, roles, created_at, expires_at)
VALUES (sqlc.arg(organization_id), sqlc.arg(email)::citext, sqlc.arg(roles)::text[], sqlc.arg(created_at)::timestamptz, sqlc.arg(expires_at)::timestamptz)
ON CONFLICT (email) WHERE status = 'pending' DO NOTHING;

-- name: VerifySeedAdminBinding :one
-- One snapshot, bound parameters, no writes or identity attributes in the result.
SELECT
    (SELECT count(*) FROM users WHERE email = sqlc.arg(email)::citext) AS users,
    (SELECT count(*) FROM users WHERE cognito_sub = sqlc.arg(subject)::text) AS subject_users,
    (SELECT count(*) FROM users u JOIN organizations o ON o.id = u.organization_id
     WHERE u.email = sqlc.arg(email)::citext AND u.cognito_sub = sqlc.arg(subject)::text
       AND u.status = 'active' AND u.is_admin AND o.name = 'Default Organization') AS matching_active_admin,
    (SELECT count(*) FROM invitations i JOIN users u ON u.email = i.email AND u.organization_id = i.organization_id
     WHERE u.email = sqlc.arg(email)::citext AND u.cognito_sub = sqlc.arg(subject)::text
       AND i.status = 'accepted' AND i.accepted_at IS NOT NULL) AS accepted_invitations;
