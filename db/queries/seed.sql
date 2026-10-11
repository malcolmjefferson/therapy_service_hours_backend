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
