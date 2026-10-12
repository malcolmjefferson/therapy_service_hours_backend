# Status: therapy_service_hours_backend

> Ticket status for this repo. Updated in the same PR that changes a ticket's state (CLAUDE.md §0.10).
> Tickets: `docs/jira/`. Other repos: [frontend](https://github.com/malcolmjefferson/therapy_service_hours_frontend/blob/main/STATUS.md), [infra](https://github.com/malcolmjefferson/therapy_service_hours_infra/blob/main/STATUS.md).

## Summary (2026-10-11)

- **Slice S0 (walking skeleton):** 9 done, 0 in review, 0 in progress, 0 not started (of 9 tickets touching this repo).
- **AWS:** dev deployed end to end by the pipeline on 2026-10-11 (data, migrate, api, web, smoke tests). Prod deploys are deferred to milestone M3 (owner decision 2026-10-11).
- **S1:** execution started; ADR-0008–0011 accepted by the owner on 2026-10-11.

## Tickets

| ID | Title | Status | PRs | Notes |
|---|---|---|---|---|
| S0-01 | Create repos, branch protection and project scaffolding | done | [backend #1](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/1) | Branch protection turned on by the owner |
| S0-02 | Apply accepted decisions: CLAUDE.md updated for ADR-0001 to ADR-0006 | done | [backend #6](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/6) | Also records public repos and GitHub Actions PR gates |
| S0-03 | Backend skeleton: Go module, chi router, /healthz, slog, request IDs | done | [backend #2](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/2) |  |
| S0-04 | OpenAPI v0 with oapi-codegen, spectral and oasdiff in backend CI | done | [backend #3](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/3) |  |
| S0-08 | Database role bootstrap custom resource (app_rw, migrator, rds_iam) | done | [backend #8](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/8), [backend #10](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/10), [backend #11](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/11), [backend #13](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/13) | Deployed to dev 2026-10-10: roles created by the provider and worker; no password in any log |
| S0-09 | Backend DB package: pgx pool with IAM token refresh and TLS | done | [backend #4](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/4), [backend #15](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/15) | IAM connect timeout raised to 12 s after the first pipeline run (RDS auth proxy slow on first use) |
| S0-10 | Migrate Lambda with embedded goose and baseline migration | done | [backend #7](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/7), [backend #15](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/15) | Retries its first connection up to 3 times (first pipeline run timed out connecting) |
| S0-17 | CI security and quality gates | done | [backend #5](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/5) | Dependabot ignore rules for held-back majors: frontend #10, infra #9 (in review) |
| S0-21 | Runbook v0 and README per repo | done | [backend #12](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/12), infra and frontend PRs | New-developer quick start and the Lambda build; runbook lives in infra |

Statuses: **not started**, **in progress**, **in review** (PR open), **done** (merged to main).

## S1 execution (2026-10-11)

| ID | Title | Status | PRs | Notes |
|---|---|---|---|---|
| S1-20 | Additional admin recovery | done | [backend #26](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/26), [infra #38](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/38) | Backend #26 and infra #38 merged. Recovery tests and runbook pass; the owner elected to retain the sole dev admin. No second live identity or recovery drill is claimed. |
| S1-02 | Auth design decisions | done | [#17](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/17) | Owner accepted ADR-0008–0011 on 2026-10-11. Live federation and latency proof remain S1-18. |
| S1-15 | Authorizer claims through the Lambda adapter | done | [#18](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/18) | Released to dev. Protected route integration follows in S1-06. |
| S1-05 | OpenAPI /me and shared Problem responses | done | [#19](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/19) | Released with frontend S1-19; route implementation remains S1-08. |
| S1-04 | Identity schema and generated queries | done | [#20](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/20) | Identity migration applied in the successful dev release 2026-10-11. PostgreSQL 17 lifecycle and uniqueness tests pass. |
| S1-17 | Read-only pre-sign-up database role | done | [#21](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/21) | Released to dev with infra #31. Exact SELECT-only grants tested; S1-03 adds the trigger. |
| S1-03 | Invitation allowlist trigger | in progress | [#22](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/22), [infra #33](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/33) | Merged and deployed in successful dev release 7c97ec40-371c-4387-8c26-817c75450d4c. Alias READY at 1/1; synthetic uninvited event returns SIGN_UP_NOT_ALLOWED with safe logs. Policy, privacy and PostgreSQL tests pass. Auth remains disabled; actual Google federation and measured cold/warm p99 remain pending (S1-18). |
| S1-09 | Identity seed Lambda | in progress | [backend #23](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/23), [infra #35](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/35), [infra #36](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/36) | Released successfully in 2a38a300-3311-43f7-9552-d07875d6e696: three accounts/invitations; repeat through the same scoped operator created/renewed none. Synthetic invited/supervisor admission passes and uninvited admission is denied. Auth stays disabled; native Cognito fixture creation remains pending. |
| S1-06 | API identity middleware and subject binding | in progress | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Backend #24 is merged and released in successful dev execution af3e6963-7cf5-4857-bfc8-c0a524ba1355. Transactional actor resolution/binding, current database roles/status and the lazy two-connection IAM pool pass race/PostgreSQL/privacy checks. Auth stays disabled; actual Gateway/native/Google token acceptance remains with S1-14 and activation. |
| S1-07 | Central authorization policies | done | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Backend #24 is merged and released. Central active-actor, same-org self/admin/supervisor/peer policies deny by default, require accepted relationships and reject storage errors. Admin grants no supervision override. /me demonstrates the handler policy call; later slices implement relationship storage. |
| S1-08 | Current user profile endpoint | in progress | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Backend #24 is merged and released. The explicit /me DTO, no-store JSON and stable 401/403 problems pass schema/privacy tests; deployed unauthenticated /me returns 401 problem+json. Actual authenticated Gateway acceptance remains pending with activation. |
| S1-12 | Role authority and token staleness | in review | [#25](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/25) | PostgreSQL regression tests reuse unchanged trusted claims after database admin/supervisor removal and deactivation, and reject token-group elevation for ordinary users. Role-authority runbook documents next-request enforcement, in-flight request timing and S5-05 Cognito follow-up. Review/merge remains pending; actual Gateway token acceptance is separately gated on activation. |
