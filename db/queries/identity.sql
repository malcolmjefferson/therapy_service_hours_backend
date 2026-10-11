-- name: GetUserByEmail :one
SELECT u.*, o.name AS organization_name
FROM users u JOIN organizations o ON o.id = u.organization_id
WHERE u.email = sqlc.arg(email)::citext;

-- name: GetUserBySub :one
SELECT u.*, o.name AS organization_name
FROM users u JOIN organizations o ON o.id = u.organization_id
WHERE u.cognito_sub = sqlc.arg(cognito_sub);

-- name: GetPendingInvitationByEmail :one
SELECT * FROM invitations
WHERE email = sqlc.arg(email)::citext AND status = 'pending' AND expires_at > sqlc.arg(at)::timestamptz;

-- name: GetDefaultOrganization :one
SELECT * FROM organizations WHERE name = 'Default Organization';
