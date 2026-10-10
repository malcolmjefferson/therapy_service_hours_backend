# therapy_service_hours_backend

Go API (AWS Lambda), migrations, SQL queries and the **OpenAPI contract** (`api/openapi.yaml`, source of truth). Part of the **Therapy Service Hours Tracker** (see [CLAUDE.md](CLAUDE.md)).

## Start here

- **Specification and working agreements:** [CLAUDE.md](CLAUDE.md)
- **Engineering standards** (rules block merges): [docs/standards/](docs/standards/README.md)
- **Decisions:** [docs/adr/](docs/adr/README.md)
- **Backlog:** [docs/jira/](docs/jira/README.md)

## First-time setup

1. Install [gitleaks](https://github.com/gitleaks/gitleaks) (secret scanning, SEC-61).
2. Turn on the repo's git hooks once per clone:

   ```bash
   git config core.hooksPath .githooks
   ```

   The pre-commit hook scans staged changes for secrets and **blocks the commit** if gitleaks is missing.

## Commands

Requires Go 1.27 (pinned in `go.mod`).

| Command | What it does |
|---|---|
| `go test ./...` | Unit and integration tests. Integration tests start Postgres with Testcontainers and **skip when Docker is not running**. On Docker Desktop for Windows set `TESTCONTAINERS_RYUK_DISABLED=true`. CI adds `-race`. |
| `go vet ./...` | Static checks |
| `APP_VERSION=local LOCAL_HTTP_ADDR=127.0.0.1:8080 go run ./cmd/api` | Serve the API locally; then open `http://127.0.0.1:8080/api/v1/healthz` |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bootstrap ./cmd/api` | Build the Lambda binary (`provided.al2023`, arm64) |

## Configuration (environment variables, set by CDK)

| Variable | Required | Meaning |
|---|---|---|
| `APP_VERSION` | Yes | Build identifier reported by `/api/v1/healthz` |
| `LOG_LEVEL` | No (`info`) | `debug`, `info`, `warn` or `error` |
| `MAX_BODY_BYTES` | No (1 MiB) | Largest accepted request body |
| `LOCAL_HTTP_ADDR` | No | Local development only: serve plain HTTP instead of the Lambda runtime |

## Contributing

- `main` is protected: changes go through a pull request with passing checks (ARC-49).
- Work on a branch named after the ticket, for example `feat/S0-03-backend-skeleton`.
- Fill in the PR template, including the standards checklists.
- Client data is **pseudonyms or initials only**, everywhere (CLAUDE.md §0.7).
