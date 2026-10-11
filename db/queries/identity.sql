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

-- name: CheckPreSignUpAdmission :one
-- One snapshot; presignup_ro needs only its two SELECT grants, not organizations.
SELECT EXISTS (
    SELECT 1 FROM invitations
    WHERE email = sqlc.arg(email)::citext AND status = 'pending' AND expires_at > sqlc.arg(at)::timestamptz
) AS invited,
EXISTS (
    SELECT 1 FROM users WHERE email = sqlc.arg(email)::citext AND status = 'deactivated'
) AS deactivated;
