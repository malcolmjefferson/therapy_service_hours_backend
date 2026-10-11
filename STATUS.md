# Status: therapy_service_hours_backend

> Ticket status for this repo. Updated in the same PR that changes a ticket's state (CLAUDE.md §0.10).
> Tickets: `docs/jira/`. Other repos: [frontend](https://github.com/malcolmjefferson/therapy_service_hours_frontend/blob/main/STATUS.md), [infra](https://github.com/malcolmjefferson/therapy_service_hours_infra/blob/main/STATUS.md).

## Summary (2026-10-10)

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
| S1-02 | Auth design decisions | in review | [#17](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/17) | Owner accepted ADR-0008–0011 on 2026-10-11. Live federation and latency proof remain S1-18. |
| S1-15 | Authorizer claims through the Lambda adapter | in review | [#18](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/18) | Trusted context transport, fail-closed middleware, and Lambda/local HTTP startup guard implemented. Focused tests pass; protected route integration follows in S1-06. |
| S1-05 | OpenAPI /me and shared Problem responses | in review | [#19](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/19) | Spectral and contract tests pass. Must release with frontend S1-19; route implementation remains S1-08. |
| S1-04 | Identity schema and generated queries | in review | [#20](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/20) | PostgreSQL 17 integration tests pass: uniqueness, invitation lifecycle, expiry, typed lookups and migration idempotence. Live migrate-Lambda application remains pending. |
| S1-17 | Read-only pre-sign-up database role | in review | [#21](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/21) | PostgreSQL 17 permission tests pass. Paired with infra #31; live bootstrap and S1-03 trigger attachment remain pending. |
