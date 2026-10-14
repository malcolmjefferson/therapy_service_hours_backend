# ADR-0012: The category lookup is the one API response a browser may cache

- **Status:** Accepted · **Date:** 2026-10-14 (accepted 2026-10-14) · **Deciders:** project owner
- **Related:** FR-LOG-07, SEC-33 · Tickets: S2-03, S2-08

## Context

SEC-33 is a **MUST**: every API response sets `Cache-Control: no-store`. The `apiHeaders` middleware
does this for all routes, so a new endpoint is uncacheable by default.

S2-08 asks for the opposite on one route: *"`GET /categories` returns grouped lookup data with
Cache-Control"*, with the acceptance criterion *"Cacheable response headers"*. The contract merged in
S2-03 already documents `Cache-Control: private, max-age=3600` on that response. The data is the 12
seeded categories (CLAUDE.md §8). It is the same for every user, holds no personal or client data,
and changes only with a migration. The entry form and the entries table both need it on every page
load, so `no-store` means a request that returns identical bytes each time.

Departing from a MUST needs an ADR.

## Decision

`GET /api/v1/categories` answers `Cache-Control: private, max-age=3600`. **Every other route keeps
`no-store`.**

- The handler sets the header on the 200 response only. The middleware default still applies to
  every error response from that route (401, 403, 500), so a problem is never cached.
- `private` keeps shared caches out. CloudFront's `/api/*` behavior has caching disabled (ADR-0004)
  and nothing else sits between the browser and the API.
- The exception is the single constant `categoriesCacheControl` in `internal/http/categories.go`. A
  second cacheable route needs its own ADR; it is not covered by this one.

## Alternatives considered

| Option | Why not |
|---|---|
| Keep `no-store` and change the S2-03 contract and S2-08 ticket | The frontend gains nothing from refetching 12 fixed rows on every page, and the owner reviewed the contract with the header in it. |
| `no-cache` with an `ETag` | Still a request per page load, and adds conditional-request code for a list that changes at migration time. |
| Bundle the categories into the frontend build | The ids are database-generated UUIDs that differ per environment, so they cannot be compiled in. |
| `public, max-age=...` | Allows shared caches. Nothing here is secret, but the route is behind the authorizer and has no reason to be shared. |

## Consequences

- **Positive:** the entry form and table do not refetch an unchanging list; the contract and ticket
  are honoured.
- **Negative:** after a migration that adds or renames a category, a browser can show the old list for
  up to an hour (reload does not bypass `max-age` in every browser). Categories change rarely and
  only by migration, so this is accepted. A category change that must show immediately should lower
  `max-age` in the same release.
- **Cost:** none.

## Validation

- A handler test asserts the exact header on the 200 and `no-store` on 401, 403 and 500 from the same
  route, and on a neighbouring route (`GET /clients`).
- The response is validated against `api/openapi.yaml`.

## Open items

- None. `docs/standards/04-security-standards.md` SEC-33 stays as written; this ADR is its one
  recorded exception.
