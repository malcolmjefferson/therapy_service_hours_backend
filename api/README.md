# API contract

`api/openapi.yaml` (OpenAPI 3.1) is the **source of truth** for the API (ADR-0006). Change it **first**, before the handler or the client (CLAUDE.md §0.5, GO-52).

## Workflow for a contract change

S2-03 defines the log-hours contract (categories, clients, entries) before S2-05 to S2-08
implement it; S2-06 and S2-07 have implemented `listEntries` and the client operations. The generator excludes the operations that are not implemented yet from route generation
(`exclude-operation-ids` in `api/oapi-codegen.yaml`) while retaining their models, and the embedded
server spec is filtered too; the full published contract remains `api/openapi.yaml`. When a ticket
implements an operation, it removes that operation from the list, implements the strict handler and
regenerates. There are no placeholder handlers behind this contract-only change. The clients
operations (S2-07) are implemented and no longer excluded; categories and entries still are.

Request bodies are checked against the request schema before a handler runs
(`internal/http/bodyschema.go`), so `additionalProperties: false`, `required`, types and lengths in
the spec are enforced at runtime. A violation is a `400` validation problem that names the field but
never echoes the value.

Merge each frontend pinned-spec/types/handler update with its contract in one release; the
pipeline's drift gate checks the backend commit and pinned frontend copy byte for byte.

1. Edit `api/openapi.yaml`.
2. Regenerate the Go server: `go generate ./...` (writes `internal/http/gen/api.gen.go`, which is committed).
3. Implement the new method of `gen.StrictServerInterface` in `internal/http`. The build fails until you do.
4. Run the gates locally:
   - `scripts/check-generated.sh`: generated code is current
   - `scripts/check-spectral.sh`: lint plus ruleset self-test (needs Node 22)
   - `scripts/check-breaking.sh <old-spec>`: no breaking change
   - `go test ./...`: includes the contract tests, which validate real responses against the spec
5. In the frontend repo, `npm run api:sync` updates the pinned copy (S0-19).

## Conventions

| Topic | Rule |
|---|---|
| **Paths** | Base `/api/v1` (the `servers` URL). Kebab-case nouns, `{camelCase}` parameters; actions are sub-resources such as `POST /entries/{entryId}/approve` (ARC-13). |
| **Fields** | `snake_case` JSON properties. |
| **Errors** | Every operation's `default` response is `#/components/responses/Problem` (`application/problem+json`, RFC 7807). `type` is a stable URN, `urn:therapy-service-hours:problem:<kind>`; clients branch on it, never on `title` (ARC-15). Validation problems list fields in `errors`. Every problem carries `request_id`. |
| **Pagination** | List endpoints use the shared `page` (1-based) and `page_size` (default 50, maximum 200) parameters and return `items` plus `page` (`PageInfo`). Order is stable with `id` as the tiebreaker (GO-56, ARC-16). |
| **Idempotency** | Every `POST` takes the shared `Idempotency-Key` header (a UUID, one per user action, reused on retry). Same key and body returns the original result; same key with a different body returns 409 (GO-57, ARC-17). A `POST` that changes nothing may opt out with `x-idempotency: not-required` and say why in its description. |
| **Limits** | Every free-text string has `maxLength` and every array has `maxItems` (SEC-26). |
| **Auth** | `cognitoJwt` is required globally. Only public operations declare `security: []` (today only `GET /healthz`). |
| **Audiences** | Responses are explicit, per-audience schemas. Peer and supervisor schemas never reference the owner's entry schema (ARC-18, SEC-40). |
| **Log-hours problems** | `409` `urn:therapy-service-hours:problem:entry-overlap` (with `conflicting_entry`), `entry-locked` (approved entry), `client-name-taken` (with `existing_client`) and `client-in-use` (with `entry_count`). Unknown request fields, including `hours`, are `400` `validation`. Another user's entry or client is `404`, never `403`. |
| **Compatibility** | Within v1, only add optional fields, new endpoints, or enum values the frontend handles. Anything `oasdiff` reports as breaking needs the `breaking-change` PR label, and both sides must ship in the same release (ARC-14). |

## Spectral ruleset (`.spectral.yaml`)

Extends `spectral:oas` (recommended) and fails on **warnings**. Each custom rule has a fixture in `api/testdata/spectral/<rule>.yaml` that must fail with exactly that rule; `valid.yaml` must pass.

| Rule | Severity | What it enforces |
|---|---|---|
| `operation-operationId`, `operation-tags` | error | Every operation is named and tagged (names become generated functions) |
| `tsh-operation-id-camel-case` | error | `operationId` is camelCase |
| `tsh-path-kebab-case` | error | Path segments are kebab-case or `{camelCase}` parameters |
| `tsh-properties-snake-case` | error | Schema properties are snake_case |
| `tsh-default-problem-response` | error | `default` response is the shared Problem |
| `tsh-string-max-length` | error | Strings have `maxLength`, except enums and date/time formats |
| `tsh-array-max-items` | error | Arrays have `maxItems` |
| `tsh-list-is-paginated` | error | A GET returning `items` takes `page` and `page_size` |
| `tsh-post-idempotency-key` | error | A POST takes `Idempotency-Key` unless it opts out with `x-idempotency: not-required` |
| `info-contact` | off | Private, single-owner API |
| `oas3-unused-component` | off | Shared conventions are defined before the first endpoint uses them |

## CI (`.github/workflows/contract.yml`)

| Job | Fails when |
|---|---|
| `codegen` | `go generate` changes anything, or a contract test fails |
| `spectral` | the spec breaks a rule, or a rule stops catching its fixture |
| `breaking` | `oasdiff` finds a breaking change against the PR's base branch (skipped with the `breaking-change` label); its self-test also proves a removed endpoint is caught |
