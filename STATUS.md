# Status: therapy_service_hours_backend

> Ticket status for this repo. Updated in the same PR that changes a ticket's state (CLAUDE.md §0.10).
> Tickets: `docs/jira/`. Other repos: [frontend](https://github.com/malcolmjefferson/therapy_service_hours_frontend/blob/main/STATUS.md), [infra](https://github.com/malcolmjefferson/therapy_service_hours_infra/blob/main/STATUS.md).

## Summary (2026-10-10)

- **Slice S0 (walking skeleton):** 8 done, 1 in review, 0 in progress, 0 not started (of 9 tickets touching this repo).
- **AWS:** account bootstrapped for CDK in us-west-2 (CDKToolkit). First dev deploy of the data stack (VPC, RDS, database roles) is pending; nothing else is deployed.
- **Next:** S0-11 (API) and S0-12 (web) constructs, then the pipeline (S0-14 to S0-16) and alerts and budget (S0-18).

## Tickets

| ID | Title | Status | PRs | Notes |
|---|---|---|---|---|
| S0-01 | Create repos, branch protection and project scaffolding | done | [backend #1](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/1) | Branch protection turned on by the owner |
| S0-02 | Apply accepted decisions: CLAUDE.md updated for ADR-0001 to ADR-0006 | done | [backend #6](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/6) | Also records public repos and GitHub Actions PR gates |
| S0-03 | Backend skeleton: Go module, chi router, /healthz, slog, request IDs | done | [backend #2](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/2) |  |
| S0-04 | OpenAPI v0 with oapi-codegen, spectral and oasdiff in backend CI | done | [backend #3](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/3) |  |
| S0-08 | Database role bootstrap custom resource (app_rw, migrator, rds_iam) | in review | [backend #8](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/8), [backend #10](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/10), [backend #11](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/11), fix: backend PR (this one) | Third dev deploy: the RDS master (not a superuser) could not set default privileges for migrator; fixed with a temporary membership, and the tests now run the bootstrap as a non-superuser |
| S0-09 | Backend DB package: pgx pool with IAM token refresh and TLS | done | [backend #4](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/4), fix: backend PR (this one) | IAM connect timeout raised to 12 s after the first pipeline run (RDS auth proxy slow on first use) |
| S0-10 | Migrate Lambda with embedded goose and baseline migration | done | [backend #7](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/7), fix: backend PR (this one) | Retries its first connection up to 3 times (first pipeline run timed out connecting) |
| S0-17 | CI security and quality gates | done | [backend #5](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/5) | Dependabot ignore rules for held-back majors: frontend #10, infra #9 (in review) |
| S0-21 | Runbook v0 and README per repo | done | [backend #12](https://github.com/malcolmjefferson/therapy_service_hours_backend/pull/12), infra and frontend PRs | New-developer quick start and the Lambda build; runbook lives in infra |

Statuses: **not started**, **in progress**, **in review** (PR open), **done** (merged to main).
