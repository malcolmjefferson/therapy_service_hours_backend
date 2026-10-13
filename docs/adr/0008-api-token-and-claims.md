# ADR-0008: ID token as the API credential, claims only from the authorizer

- **Status:** Accepted · **Date:** 2026-10-11 (accepted 2026-10-11) · **Deciders:** project owner
- **Related:** FR-AUTH-03, FR-AUTH-04, SEC-03, SEC-04, SEC-35 · Tickets: S1-02, S1-05, S1-06, S1-14, S1-15, S1-10

## Context

Three separate gaps in the S1 plan turn out to be one decision about tokens.

1. **Which token.** `api/openapi.yaml` describes the credential as a Cognito **access token**.
   Cognito access tokens carry `sub`, `client_id`, `scope` and `cognito:groups` — they do **not**
   carry `email`. FR-AUTH-03 binds a federated identity to a **pre-existing** `users` row on first
   sign-in, which is by definition the one request where `cognito_sub` is not yet in the database.
   With an access token there is nothing left to match on, so first sign-in can never bind.
2. **Where claims come from.** `internal/apigw/adapter.go:41` copies path, method, query, body,
   headers and cookies into the `http.Request` and drops `event.RequestContext.Authorizer`
   entirely, so the middleware has no validated claims to read. Validating the bearer token in the
   handler instead is not available: that needs Cognito's JWKS over the internet, and the API Lambda
   has no NAT. Reading claims from the `Authorization` header without validating them would be an
   authentication bypass.
3. **Which routes are protected.** SEC-03 requires the authorizer on every route except `/healthz`,
   and the spec encodes that with `security: []` on `/healthz`. But API Gateway attaches authorizers
   **per route**, and S0 built exactly one — `ANY /api/v1/{proxy+}` (`lib/app/api.ts:103`). One
   authorizer there protects `/healthz` too, and the dev smoke gate calls `/healthz` and an unknown
   API path without a token (`scripts/smoke.sh:32`, `e2e/smoke.spec.ts:30`), so every pipeline run
   would fail.

## Decision

- The SPA sends the **ID token** as the bearer credential. It carries `email`, `email_verified`,
  `given_name`, `family_name` and `cognito:groups`, which is what first-sign-in binding and `/me`
  need. The API Gateway JWT authorizer is configured with the user pool as issuer and the app client
  id as audience, so `aud` is checked.
- **Binding** uses `email` (citext, unique) on first sign-in and `cognito_sub` on every later
  request, written once inside the binding transaction with `ON CONFLICT`, never read-then-write.
- The API reads claims **only** from `event.RequestContext.Authorizer.JWT.Claims`, carried into the
  request context by the adapter (S1-15). A request that arrives with no authorizer context is
  refused with 401. The handler never parses the `Authorization` header for identity, and the local
  development path cannot supply claims when running on Lambda.
- **Routes:** `GET /api/v1/healthz` gets its own unauthenticated route; the authorizer goes on the
  proxy route (S1-14). The unknown-API-path release gate becomes "401 without a token, problem+json
  404 with one", and a synth test asserts exactly one unauthenticated route exists.
- **Authority:** roles come from the `users` row. `cognito:groups` is advisory in S1 and grants
  nothing, because nothing assigns Cognito groups until S5-05 and the seeded first admin has no
  Cognito user at all until their first sign-in.
- **Token lifetimes** follow SEC-04 (access and ID 60 minutes, refresh 7 days) with **token
  revocation enabled** on the app client; sign-out hits the hosted UI logout endpoint and revokes
  the refresh token.
- **Storage:** tokens stay in memory (SEC-35, FE-61). A page reload therefore has no refresh token
  and relies on the hosted-UI session cookie for a silent redirect re-auth. That is accepted, and
  S1-10 covers it with a test so the behaviour is deliberate rather than discovered.
- The API cannot shape the authorizer's own rejection: HTTP APIs do not support custom gateway
  responses, so an unauthenticated call returns `{"message":"Unauthorized"}` as `application/json`,
  not `problem+json`. The spec says so, and the frontend tolerates it.

## Alternatives considered

| Option | Why not |
|---|---|
| Keep the access token, match on `sub` only | Then something must write `cognito_sub` before the first API call, which means the identity Lambda and Cognito admin APIs — S5-05 pulled forward into S1. |
| Keep the access token and add `email` via a custom scope or resource server | Cognito does not put `email` in an access token; a resource server adds scopes, not identity claims. |
| Validate the JWT in the Lambda | Needs JWKS over the internet; no NAT (CLAUDE.md §4.1). Embedding a key set adds a rotation failure mode for no gain over the authorizer. |
| Distribution-wide authorizer and a public path exception at CloudFront | CloudFront cannot exempt a path from an API Gateway authorizer; the authorizer is per route. |
| Move `/healthz` outside `/api/v1` | Changes the published contract and the smoke gate for no benefit; a second route costs nothing. |

## Consequences

**Positive:** first sign-in can bind; claims are always validated; `/healthz` stays public so the
release gates keep working; one place decides roles.
**Negative:**
- ID tokens are slightly larger and are the wrong tool if a machine-to-machine client is ever added;
  that client would need its own route and scheme.
- `email` is an identifier in the token, so SEC-50 (never log emails or token material) matters more;
  S1-15's acceptance criteria say so explicitly.
- Reload re-auth depends on a Cognito cookie; if the user signs out of Google elsewhere, a reload
  bounces them to sign-in.

**Cost:** $0.

## Validation

- S1-18: a first federated sign-in creates one Cognito identity and binds one `cognito_sub`; the
  second sign-in reuses both.
- S1-06: concurrent first requests do not duplicate; a role present only in the token grants nothing.
- S1-14: `scripts/smoke.sh` passes unchanged; a synth test counts unauthenticated routes.
- S1-15: adapter tests for a claims-present and a claims-absent event.

## Dev proof checkpoint (2026-10-12, S1-18)

Activation execution `248fa024-49a0-4ebc-bc76-73a236ca9aaa` succeeded. Real native
ID tokens pass Gateway and database-backed profile acceptance for the invited fixture
and refuse the uninvited fixture; both corresponding deployed-browser cases pass.
These tests do not prove Google federation or hosted-cookie logout.

The owner's real Google consent/callback attempt was refused by the pre-sign-up
guard before database admission. In this case-insensitive pool, Cognito lowercases
the entire federated username, including the provider label ([AWS mapping documentation](https://docs.aws.amazon.com/cognito/latest/developerguide/cognito-user-pools-specifying-attribute-mapping.html)).
Two otherwise-identical synthetic events against a still-pending supervisor invitation
reproduced uppercase `Google_` allow and lowercase `google_` refusal on version 3.
Backend #28 compares only the provider label without case sensitivity and preserves
the username, verified-email requirement and invitation authority. Actual first/repeat
Google identity creation/binding and logout must be rechecked after that release.

### Post-fix acceptance (2026-10-12)

Backend #28 merged and deployed in successful execution
`11d38ada-a392-471f-bf45-81592cedf9a5`; the live trigger is version 4.
The owner reported successful real Google sign-in. Browser verification then
observed the database-backed admin profile, sign-out returning through hosted
logout to the signed-out landing, repeat Google sign-in and page-reload
reauthentication. No owner credentials or callback material were retained.
Screenshots remain local and are not included in the public repository.

The repeat enabled seed step reported zero accounts, invitations, renewals and
native Cognito users created. A read-only Cognito check after repeat sign-ins
finds exactly one matching, enabled federated Google identity with verified
email; only counts and booleans were retained. Native invited/uninvited Gateway acceptance and
deployed browser rendering were verified separately. This establishes functional
real-owner sign-in and logout; it does not independently count the persisted
owner binding or demonstrate real uninvited/deactivated Google accounts.
Those evidence items and representative cold p99 remain open in S1-18.

### Binding proof preparation and release automation (2026-10-12)

Backend #25 and frontend #22 merged. Combined dev execution
`d41c4fb4-4e96-42d8-98bc-67da2a878869` succeeded, with both native browser cases
explicitly passing in pipeline smoke and no accounts/invitations/renewals/native
users created by the repeated seed step.

Backend #29 merged an IAM-only, dev-only read operation for the existing seed
Lambda. A read-only SQL snapshot returns matching user/subject/admin/accepted
invitation counts. The operator helper obtains the Google subject from the
validated dev pool without exposing it; verification never applies a seed plan
or repairs an account. The event omits the legacy admin email so older binaries
refuse before storage. See the [verification runbook](../runbooks/dev-binding-verification.md).
Its dev release `7969a830-cd94-41da-bf97-6693b389e17d` is running. Actual persisted
owner binding remains unverified until this operation releases
and is run before and after another real sign-in. Cold p99 and real Google
uninvited/deactivated demo remain separate open evidence items.

### Persisted binding and real uninvited denial (2026-10-12)

Release `7969a830-cd94-41da-bf97-6693b389e17d` succeeded with backend #29.
Both native browser cases passed in pipeline smoke, and repeat seeding created
or renewed nothing. Read-only owner checks before and after another real Google
sign-in each returned one Google identity and exactly one user, subject match,
active admin and accepted invitation. The counts remained unchanged; no identity
attributes or credentials were retained in the proof results.

A separate uninvited Google account completed basic identity consent and reached
the application's access-denied view. It had zero Cognito identities before and
after the attempt. The corresponding safe trigger log records
`SIGN_UP_NOT_ALLOWED`, handler duration 69 ms, version 5. Screenshots remain local
outside the public repositories. This establishes the invited/repeated binding
and uninvited refusal paths. It does not establish cold-start p99 or the separate
returning-deactivated-account slice demo.

Update (2026-10-13): the owner reports that the returning-deactivated-account demo
passed on dev with a second real Google account. No account identifier or
screenshot is committed. Cold-start p99 is still open.

## Remaining open items

- Revisit if a machine client ever needs the API (it would use the access token and its own routes).
