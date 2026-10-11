# therapy_service_hours_backend

Go API (AWS Lambda), migrations, SQL queries and the **OpenAPI contract** (`api/openapi.yaml`, source of truth). Part of the **Therapy Service Hours Tracker** (see [CLAUDE.md](CLAUDE.md)).

## Start here

- **Specification and working agreements:** [CLAUDE.md](CLAUDE.md)
- **Engineering standards** (rules block merges): [docs/standards/](docs/standards/README.md)
- **Decisions:** [docs/adr/](docs/adr/README.md)
- **Backlog:** [docs/jira/](docs/jira/README.md)
- **Operations** (deploy, alarms, restore, adding a migration): [infra runbook](https://github.com/malcolmjefferson/therapy_service_hours_infra/blob/main/docs/runbook.md)

## Quick start (new developer)

**Prerequisites**

| Tool | Why |
|---|---|
| [Go](https://go.dev/dl/) 1.24 or later | Any recent Go works: `go.mod` pins the toolchain (1.27) and Go downloads it on first use (`GOTOOLCHAIN=auto`, the default) |
| [Docker](https://docs.docker.com/get-docker/) (Desktop or Engine), running | Integration tests start PostgreSQL 17 with Testcontainers. Without Docker they **skip**; unit tests still run |
| [gitleaks](https://github.com/gitleaks/gitleaks) | Secret scanning in the pre-commit hook (SEC-61) |

**Steps**

```bash
git clone https://github.com/malcolmjefferson/therapy_service_hours_backend.git
cd therapy_service_hours_backend
git config core.hooksPath .githooks   # once per clone: the pre-commit hook blocks commits with secrets (and when gitleaks is missing)
go test ./...                          # unit + integration tests; first run downloads the toolchain and the Postgres image
APP_VERSION=local LOCAL_HTTP_ADDR=127.0.0.1:8080 go run ./cmd/api
```

Then open http://127.0.0.1:8080/api/v1/healthz: `{"status":"ok","version":"local"}`. The frontend's dev server (`npm run dev`) proxies `/api` to this address.

**Windows with Docker Desktop:** run `export TESTCONTAINERS_RYUK_DISABLED=true` (Git Bash) before `go test`, or the integration tests time out starting Testcontainers' Ryuk container.

## Commands

Requires Go 1.27 (pinned in `go.mod`).

| Command | What it does |
|---|---|
| `go test ./...` | Unit and integration tests. Integration tests start Postgres with Testcontainers and **skip when Docker is not running**. On Docker Desktop for Windows set `TESTCONTAINERS_RYUK_DISABLED=true`. CI adds `-race`. |
| `go vet ./...` | Static checks |
| `go generate ./...` | Regenerate `internal/http/gen` from `api/openapi.yaml` |
| `scripts/check-generated.sh`, `scripts/check-spectral.sh`, `scripts/check-breaking.sh` | API contract gates; see [api/README.md](api/README.md) |
| `APP_VERSION=local LOCAL_HTTP_ADDR=127.0.0.1:8080 go run ./cmd/api` | Serve the API locally; then open `http://127.0.0.1:8080/api/v1/healthz` |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bootstrap ./cmd/api` | Build one Lambda binary (`provided.al2023`, arm64) |

## Build the Lambdas

The pipeline builds every Lambda; a manual deploy (see the [runbook](https://github.com/malcolmjefferson/therapy_service_hours_infra/blob/main/docs/runbook.md#2-deploy-flow)) needs the same layout, which infra reads from `BACKEND_DIST`:

```bash
for c in api migrate dbbootstrap dbbootstrap-provider presignup; do
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "dist/$c/bootstrap" "./cmd/$c"
done
git rev-parse HEAD > dist/VERSION   # reported by /api/v1/healthz
```

Build from a clean checkout of `origin/main`.

## Configuration (environment variables, set by CDK)

| Variable | Required | Meaning |
|---|---|---|
| `APP_VERSION` | Yes | Build identifier reported by `/api/v1/healthz` |
| `LOG_LEVEL` | No (`info`) | `debug`, `info`, `warn` or `error` |
| `MAX_BODY_BYTES` | No (1 MiB) | Largest accepted request body |
| `LOCAL_HTTP_ADDR` | No | Local development only: serve plain HTTP instead of the Lambda runtime; startup rejects it when Lambda runtime markers are present |

## Trusted authorizer claims (S1-15)

The Lambda adapter copies only `RequestContext.Authorizer.JWT.Claims` into a private context key.
Read them with `apigw.JWTClaimsFrom(ctx)`; it returns a copy and cannot be populated by HTTP headers.
An event without a JWT authorizer clears any inherited identity. Local HTTP requests have no trusted
claims, and local HTTP mode cannot start in a Lambda environment.

Protected route middleware uses `httpapi.RequireAuthorizerClaims` before resolving the actor. It
returns a generic 401 problem with `WWW-Authenticate: Bearer` when trusted subject claims are absent.
It does not log token material or personal attributes. S1-06 mounts this guard with DB actor
resolution; the current public health route and release smoke behavior stay as deployed until S1-14.

Verify with `go test ./internal/apigw ./internal/config ./internal/http`.

The pre-sign-up database role (`presignup_ro`, S1-17) has IAM login, CONNECT, schema USAGE and
SELECT on exactly `users` and `invitations`. Bootstrap creates the role before migration; migration
00002 supplies exact table grants, and bootstrap can grant them if the tables already exist.
There is no SELECT default privilege on future tables. Verify with Docker running:
`go test ./internal/dbroles`. The tests emulate the non-superuser RDS master, rerun bootstrap,
apply the real schema as migrator, and deny writes and reads of unrelated tables.

## Pre-sign-up invitation gate (S1-03)

`cmd/presignup` warms a single `presignup_ro` IAM/TLS connection during Lambda initialization.
The separate pool has a 3-second connect timeout; the API keeps its 12-second timeout. The
Cognito event adapter bounds admission work to 4 seconds. It accepts verified Google federated
events only when one static sqlc query finds a pending unexpired invitation and no deactivated
user. It never creates a user, binds a subject, or accepts an invitation; S1-06 owns those writes.

Policy denials return `SIGN_UP_NOT_ALLOWED`; database or deadline failures return
`SIGN_UP_UNAVAILABLE`. Logs contain only the decision code, request ID and duration. Verification
flags are cleared: Google supplies the mapped verified email, and the trigger cannot transfer
another user's email alias. Existing Cognito users do not reach pre-sign-up again; their
deactivation must be enforced by the S1-06 API middleware.

`APP_ENV` is required (`dev` or `prod`), along with the normal database settings. Startup rejects
any DB user except `presignup_ro` and any auth mode except IAM. For ADR-0011 in dev only,
`PreSignUp_AdminCreateUser` admits exactly `test-invited`, `test-uninvited` and `test-supervisor`
with matching `<username>@example.invalid` emails. This lets S1-09 create the intentionally
uninvited native test user for API-denial tests. Ordinary native sign-up, real admin-created
users and all federated events have no such exception. Prod admits none of those native users.

Verify `go test ./internal/presignup ./internal/service/presignup ./internal/dbroles ./internal/db`
with Docker running, then `go vet ./...`, the configured golangci-lint gate, and `go tool sqlc diff`.
Merge and release this backend implementation before the paired infra PR adds the new command
to its build list. The infra alias is deployed with auth disabled before pool activation.
Measured cold/warm p99 and real Google federation remain S1-18; passing unit tests is not latency proof.

## Migrations (ADR-0003)

- SQL files live in `db/migrations/` (goose format, `-- +goose Up` only; forward-only) and are embedded into the `migrate` Lambda (`cmd/migrate`).
- The pipeline invokes `migrate` before each app deploy. It connects as the `migrator` role with an IAM token, takes a PostgreSQL advisory lock (a concurrent run waits, then finds nothing to do) and returns `{"status":"ok"|"error","applied":[...],"version":N,"error":"..."}`.
- Destructive statements (`DROP`, `ALTER ... DROP`, `TRUNCATE`, `RENAME`) are allowed only in a file with a `-- +contract` line; `scripts/check-migrations.sh` enforces this in CI.
- Local run against a container: `DB_HOST=127.0.0.1 DB_PORT=5432 DB_NAME=app DB_USER=app DB_AUTH=password DB_PASSWORD=... go run ./cmd/migrate` (invokes need a Lambda runtime; tests call `internal/migrate` directly).

The S1 identity migration creates organizations, case-insensitive unique user emails, unique nullable
Cognito subjects, and invitation history with one pending invitation per email. Invitations default
to 14 days; the allowlist query excludes accepted, revoked and expired invitations, including the
exact expiry instant. First-sign-in acceptance is implemented in S1-06. Default Organization is
seeded once by name, and rerunning the migration Lambda is a no-op.

SQL source lives in `db/queries/identity.sql`; `go generate ./...` runs the pinned sqlc v1.31.1 tool.
Generated queries live in `internal/db/identityqueries`. This approved MIT-licensed build tool
replaces handwritten query code and is not imported into the Lambda binaries. Verify schema and
queries with Docker running: `go test ./internal/migrate ./internal/db/...` and `go tool sqlc diff`.

## Identity seed Lambda (S1-09)

`cmd/seed` is invoked through IAM, with no public HTTP route. It connects as `app_rw` using
the shared two-connection IAM/TLS pool. A 45-second operation budget fits the 60-second Lambda.
The validated request has `admin_email`, `include_dev_users` and `allow_prod`; prod requires
`allow_prod: true` and always rejects dev fixtures. The deployment's `APP_ENV` is authoritative.

One transaction creates the Default Organization, admin users row and pending 14-day invitation.
Dev optionally adds `test-invited` and `test-supervisor`; `test-uninvited` has no DB row/invite.
Repeat runs preserve IDs, invitation expiry, accepted history and subject bindings. Expired pending
invitations are revoked and renewed once. Deactivation or conflicting organization/role state
aborts the entire transaction rather than overwriting account authority. Logs/results contain
only stable codes, request IDs, timing and counts; no emails or raw database errors.

The paired infra dev step runs after migrations and app deployment, with Cognito creation outside
the VPC and suppressed notifications. Release the pipeline build-discovery fix before this
backend binary, then release the backend before infra consumes `seed/bootstrap`. Verify with
`go test ./internal/service/seed ./internal/seed ./internal/migrate` (Docker PostgreSQL 17),
`go vet ./...`, the configured linter and `go tool sqlc diff`. Live seed idempotency and invitation
admission checks are recorded after the paired infra release. Cognito activation remains later work.

## CI gates (`.github/workflows/ci.yml`)

Every PR runs these in parallel; any failure blocks the merge:

| Job | Checks |
|---|---|
| `lint` | golangci-lint v2 (`.golangci.yml`): errcheck, govet, staticcheck, gosec (incl. G201/G202 string-built SQL), revive, gocritic, errorlint, wrapcheck on adapters, depguard layer rules (GO-01, GO-02), cyclop 15, gofmt, goimports |
| `test` | `go mod verify`, then `go test -race` including Testcontainers integration tests |
| `vuln` | `govulncheck` (the toolchain is pinned in `go.mod`; bump it when the standard library gets a fix) |
| `semgrep` | `semgrep/no-string-built-sql.yaml` forbids `fmt.Sprintf`, `+` or `strings.Builder` SQL in `Exec`/`Query`/`QueryRow`; `semgrep --test` proves the rule still catches its seeded fixture |
| `sqlc` | `go tool sqlc diff` for the generated identity queries (S1-04) |

Plus `contract.yml` (API contract, S0-04), `migrations.yml` (no untagged destructive SQL, S0-10) and `secret-scan.yml` (gitleaks). Dependabot opens weekly update PRs for Go modules and Actions.

## Contributing

- `main` is protected: changes go through a pull request with passing checks (ARC-49).
- Work on a branch named after the ticket, for example `feat/S0-03-backend-skeleton`.
- Fill in the PR template, including the standards checklists.
- Client data is **pseudonyms or initials only**, everywhere (CLAUDE.md §0.7).
