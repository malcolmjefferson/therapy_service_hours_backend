## Summary

<!-- One or two sentences: what changes and why. -->

## Ticket

<!-- Draft ID and link, for example S0-03. -->

## Changes

-

## How to verify

<!-- Steps a reviewer can follow, with commands and expected results (CLAUDE.md §0.4). -->

1.

## Definition of Done (CLAUDE.md §0.4, docs/jira/README.md)

- [ ] **Implementation:** acceptance criteria of the ticket met
- [ ] **Validation:** automated tests added and passing; manual steps (if any) done
- [ ] **Documentation:** reviewer steps above; `api/openapi.yaml` changed first if a contract changed; `CLAUDE.md` updated if a decision changed
- [ ] **Budget guard:** no NAT Gateway, RDS Proxy, interface endpoints, ALB/NLB, Multi-AZ, extra DB or paid service (CLAUDE.md §0.6)
- [ ] **Data guard:** pseudonyms only in fixtures, logs and docs; no secrets committed (CLAUDE.md §0.7)

## Deviations and new dependencies

<!-- SHOULD rules skipped (one-line reason each). New Go modules (GO-90) or npm packages (FE-80): why, licence, maintenance, size. Write "None" if none. -->

None

## Go review checklist (docs/standards/01-go-coding-standards.md §14)

- [ ] Layer boundaries respected (GO-01, GO-02, GO-10)
- [ ] New behavior added through new implementations or registry entries, not edits to switches (GO-13)
- [ ] Interfaces are small and declared by the consumer (GO-20, GO-21)
- [ ] Clock, ID generator and stores are injected; no `time.Now()` in services (GO-23, GO-24)
- [ ] Errors are wrapped, domain errors used, and nothing internal leaks (GO-31 to GO-34)
- [ ] SQL is in `.sql` files, bound parameters only, and transitions are conditional (GO-43 to GO-47)
- [ ] OpenAPI changed first; DTOs are explicit per audience (GO-52, GO-54)
- [ ] `authz` called; negative tests added (GO-59, GO-74)
- [ ] No sensitive values logged (GO-64)
- [ ] Unit, integration and injection tests added; fixtures fictional (GO-71 to GO-76)

## Architecture review checklist (docs/standards/03-architecture-standards.md §13)

- [ ] Fits the reference architecture, or has an ADR (ARC-01)
- [ ] No internet path from VPC Lambdas; budget-guard assertion passes (ARC-02, ARC-29)
- [ ] Use case is one transaction or an idempotent workflow (ARC-06, ARC-17)
- [ ] Invariants enforced in the schema as well as code (ARC-21)
- [ ] API change is backward compatible or labelled and coordinated (ARC-14)
- [ ] Audience-specific DTOs (ARC-18)
- [ ] Env differences only in config; tags and naming applied (ARC-28, ARC-31)
- [ ] Alarms and logs cover the new path (ARC-43, ARC-44)
- [ ] Performance target checked for new queries (ARC-26, ARC-40)

## Security review checklist (docs/standards/04-security-standards.md §13)

- [ ] New route: authz call, relationship-scoped SQL, 404-not-403 where appropriate, negative tests (SEC-10 to SEC-16)
- [ ] All SQL is static and parameterized; sort allowlisted; `LIKE` escaped (SEC-20 to SEC-25)
- [ ] Every new string field has `maxLength` in the spec (SEC-26)
- [ ] Response uses the right audience DTO; leak test added (SEC-40, SEC-41)
- [ ] No HTML injection paths; exports escaped (SEC-30, SEC-36, SEC-37)
- [ ] Nothing sensitive logged (SEC-50)
- [ ] No secrets added; config is not hard-coded (SEC-60)
- [ ] IAM, security group and bucket changes are least privilege (SEC-64 to SEC-66)
- [ ] New dependency justified and scanned (SEC-71, SEC-73)

<!-- Mark items that do not apply to this change as "n/a" rather than deleting them. -->
