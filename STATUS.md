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
| S1-20 | Additional admin recovery | in review | [backend #26](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/26), [infra #38](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/38) | Additional-admin recovery is verified against PostgreSQL in dev and explicitly opted-in prod: original binding/history preserved, repeat idempotent, missing prod flag makes no writes. Runbook has exact scoped Lambda invocation and failure handling. Owner decided 2026-10-11 to retain the sole dev admin; no additional live identity is seeded. Review/merge remains pending; no live recovery drill is claimed. |
| S1-02 | Auth design decisions | done | [#17](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/17) | Owner accepted ADR-0008–0011 on 2026-10-11. Live federation and latency proof remain S1-18. |
| S1-15 | Authorizer claims through the Lambda adapter | done | [#18](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/18) | Released to dev. Protected route integration follows in S1-06. |
| S1-05 | OpenAPI /me and shared Problem responses | done | [#19](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/19) | Released with frontend S1-19; route implementation remains S1-08. |
| S1-04 | Identity schema and generated queries | done | [#20](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/20) | Identity migration applied in the successful dev release 2026-10-11. PostgreSQL 17 lifecycle and uniqueness tests pass. |
| S1-17 | Read-only pre-sign-up database role | done | [#21](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/21) | Released to dev with infra #31. Exact SELECT-only grants tested; S1-03 adds the trigger. |
| S1-03 | Invitation allowlist trigger | in progress | [#22](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/22), [infra #33](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/33) | Warmed live alias and pool-scoped attachment are deployed; real owner sign-in exposed the uppercase-only Google prefix guard. Paired synthetic pending-supervisor events prove Google_ allows and google_ denies on version 3. Backend #28 compares only the provider prefix without case sensitivity, preserving username, verified email, invitation checks and response flags. Actual Google acceptance awaits its release. |
| S1-09 | Identity seed Lambda | in progress | [backend #23](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/23), [infra #35](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/35), [infra #36](https://github.com/malcolmjefferson/therapy_service_hours_infra/pull/36) | Database seed/admin invitation and three native dev fixtures are deployed. Native profiles and uninvited denial pass against real Gateway; recovery support/sole-admin decision are merged. Database repeat idempotency is verified; a subsequent enabled release must confirm native repeat counts remain zero. First real admin binding awaits the Google prefix repair. |
| S1-06 | API identity middleware and subject binding | in progress | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Backend identity/binding is merged and released. Real native tokens resolve invited profiles and deny uninvited accounts; transactional and privacy/race tests pass. Real Google first/repeat binding remains pending the trigger prefix repair in backend #28. |
| S1-07 | Central authorization policies | in progress | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Policy implementation prepared: active-actor and same-org self, admin, supervisor and peer policies; accepted relationships required, storage failures deny, admin grants no supervision override. /me demonstrates the required handler policy call. Later slices provide relationship storage. |
| S1-08 | Current user profile endpoint | in progress | [#24](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/24) | Backend profile endpoint is merged/released and passes actual native-token 200/403 with the explicit DTO and no-store behavior. The invited name renders in the deployed UI and uninvited users see access denied. Real Google owner profile acceptance awaits backend #28. |
| S1-18 | Federation and trigger latency evidence | in progress | [#28](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/28) | Live owner Google attempt exposed the case-insensitive provider-prefix bug, reproduced with a still-pending fictional supervisor invitation. Native/resource acceptance passes. 100 direct synthetic alias invocations produced expected admission outcomes; excluding one initialization-containing log tail leaves 99 warm samples with REPORT p50 3.03 ms, p95 43.17 ms and p99/max 54.37 ms. This is not actual federation or cold p99 proof; real first/repeat binding and representative cold measurements remain pending. |
