# Role revocation and token staleness (S1-12)

## Authority and timing

API Gateway validates the ID token's signature, issuer, audience and expiry. The
API then resolves the subject and verified email against PostgreSQL on every
protected request. The current `users` row supplies organization, active status,
`is_admin` and `is_supervisor`; `cognito:groups` supplies no application privileges.
Handlers must check the central `authz` policies using that resolved actor.

After a role change commits, the next request reads the new roles even if the
browser reuses an unexpired token with old groups. Removing admin or supervisor
privileges preserves ordinary self access for an active account. Deactivation
denies protected access with a generic 403. A database failure fails closed.
A request that already resolved its actor before the change committed can finish
with that earlier snapshot; this is next-request enforcement, not cancellation
of requests already in flight.

## Operational verification

1. Use the approved account-management workflow for the intended account and
   organization. Confirm the database mutation committed before checking access.
   Until S5 supplies that workflow, any emergency database change must use the
   existing scoped operator process and be recorded in the incident record.
2. Reuse the session's existing ID token for the next protected request; do not
   copy tokens, email addresses or credentials into logs, tickets or screenshots.
3. After demotion, `/api/v1/me` must omit the removed role and the corresponding
   central admin/supervisor policy must deny. S1 has no admin-management endpoint;
   endpoint acceptance checks will accompany the later feature slices.
4. After deactivation, `/api/v1/me` must return 403. Restore access only through an
   explicitly authorized account change; signing in again does not reactivate it.
5. If access disagrees with the committed row, stop privileged actions and inspect
   request IDs and safe denial metadata. Do not treat a token group as evidence of
   authority or reinstate roles merely to clear an error.

## Cognito follow-up

S5-05 adds Cognito group synchronization, account disable and global sign-out.
Until then, database enforcement ends application privileges on the next request;
it does not revoke the Cognito session or erase an already issued token. Cognito
group checks must never replace the database checks. Email changes also fail
closed against the existing binding and require the approved re-invitation flow.

## Regression evidence

With Docker running, execute:

```bash
TESTCONTAINERS_RYUK_DISABLED=true go test -count=1 -v ./internal/migrate -run 'TestRoleRemovalAndDeactivationOverrideUnchangedTokenClaims|TestTokenGroupsCannotElevateAnOrdinaryDatabaseUser'
```

These tests use PostgreSQL 17, the real identity store and authentication service,
and the central authorization policies. They reuse identical trusted claims after
demotion and deactivation, and try multiple group encodings on an ordinary user.
They model claims already validated by Gateway; live token acceptance remains
part of S1-14/S1-18 after auth activation. Check that both tests ran and passed:
integration tests skip when Docker is unavailable.
