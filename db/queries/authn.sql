-- name: LockIdentityEmail :exec
SELECT pg_advisory_xact_lock(hashtextextended(lower(sqlc.arg(email)::text), 917006));

-- name: GetActorBySub :one
SELECT u.*, o.name AS organization_name FROM users u
JOIN organizations o ON o.id = u.organization_id
WHERE u.cognito_sub = sqlc.arg(cognito_sub) FOR UPDATE OF u;

-- name: GetIdentityUserByEmail :one
SELECT * FROM users WHERE email = sqlc.arg(email)::citext FOR UPDATE;

-- name: GetIdentityInvitation :one
SELECT * FROM invitations WHERE email = sqlc.arg(email)::citext
AND status = 'pending' AND expires_at > sqlc.arg(at)::timestamptz FOR UPDATE;

-- name: InsertInvitedUser :execrows
INSERT INTO users (organization_id, email, first_name, last_name, is_admin, is_supervisor)
VALUES (sqlc.arg(organization_id), sqlc.arg(email)::citext, sqlc.arg(first_name), sqlc.arg(last_name), sqlc.arg(is_admin), sqlc.arg(is_supervisor))
ON CONFLICT (email) DO NOTHING;

-- name: BindIdentitySubject :execrows
UPDATE users SET cognito_sub = sqlc.arg(cognito_sub)
WHERE id = sqlc.arg(id) AND cognito_sub IS NULL AND status = 'active';

-- name: AcceptIdentityInvitation :execrows
UPDATE invitations SET status = 'accepted', accepted_at = sqlc.arg(at)::timestamptz
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > sqlc.arg(at)::timestamptz;
