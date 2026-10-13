# ADR-0007: Pre-sign-up allowlist inside the VPC, with a measured latency budget

- **Status:** Accepted · **Date:** 2026-10-11 (accepted 2026-10-11) · **Deciders:** project owner
- **Related:** FR-AUTH-01, FR-AUTH-05, SEC-02, NFR-COST-01 · Tickets: S1-03, S1-17, S1-18

## Context

FR-AUTH-01 and SEC-02 make sign-up invite-only through a Cognito **pre-sign-up trigger** that
rejects any email without an invitation. The invitations live in RDS PostgreSQL, in private subnets
with no NAT, so the trigger has to run inside the VPC to read them.

Cognito's synchronous Lambda triggers have a **fixed 5-second timeout** that cannot be raised. If the
trigger does not answer in time, Cognito fails the sign-up, which to the user is a refused sign-in.

This project already has evidence that a cold VPC Lambda cannot be trusted to connect to RDS inside
five seconds:

- `internal/db/pool.go:30` sets `IAMConnectTimeout = 12 * time.Second`.
- `internal/db/token_test.go:204` asserts that timeout must be **above 5 seconds**.
- The backend's `STATUS.md` records why (S0-09): *"IAM connect timeout raised to 12 s after the
  first pipeline run (RDS auth proxy slow on first use)."*

So the default shape of S1-03 — a cold VPC Lambda opening a fresh IAM-authenticated connection
inside Cognito's 5-second window — is expected to fail intermittently, and intermittently is the
worst way for an auth path to fail.

## Decision

Keep the allowlist in PostgreSQL and the trigger in the VPC, and buy determinism rather than a
second datastore:

1. **One source of truth.** `invitations` in PostgreSQL stays the only allowlist. No projection into
   another service, so there is no dual-write to keep consistent and no compensation logic.
2. **Provisioned concurrency of 1** on the pre-sign-up function, so the execution environment and
   its pgx connection are already warm when Cognito calls. At 128–256 MiB this is roughly
   **$1–3 per month** — **approved by the owner on 2026-10-11** — inside NFR-COST-01's headroom
   (current estimate $35–45 of $50), and it is the
   one place in this architecture where a cold start is a user-visible failure rather than a slow
   response.
3. **A connect timeout below the ceiling** for this function only — 3 seconds, not the API's 12 — so
   a slow connection fails fast and fails **closed** with a stable error code rather than being cut
   off mid-flight by Cognito.
4. **Least privilege:** a third database role, `presignup_ro`, with `SELECT` on `invitations` and
   `users` and nothing else (S1-17); the function has no internet and no write rights.
5. **Measured, not assumed.** S1-18 records cold and warm p99 trigger duration on dev against the
   5-second ceiling before the slice is called done.

If S1-18 shows the budget is still missed, the fallback is the alternative below, and this ADR is
superseded rather than quietly stretched.

## Alternatives considered

| Option | Why not |
|---|---|
| Cold VPC Lambda, no provisioned concurrency | The project's own connect timeout and S0-09's incident say this exceeds 5 s on a cold start. Fails closed, so uninvited users stay out, but invited users are refused at random. |
| Non-VPC trigger reading a DynamoDB allowlist projection | Removes the latency risk entirely and costs ~$0 at this volume, but adds a second store that must stay consistent with `invitations`, which means dual-write plus compensation in every S5 admin workflow. Kept as the documented fallback if S1-18 fails. |
| Pre-create Cognito users at invite time and skip the trigger | Needs the identity Lambda and Cognito admin APIs, which are S5-05. It would pull a whole slice forward, and self sign-up stays disabled either way. |
| Raise the Cognito trigger timeout | Not possible; the 5-second limit is fixed. |
| Allow sign-up and check the invitation at first API call | Violates SEC-02: an uninvited Google account would get a Cognito identity. |

## Consequences

**Positive:** one allowlist, no synchronisation problem, least-privilege database access, and a
latency risk that is bounded and measured rather than discovered during the demo.
**Negative:**
- A small always-on cost, and the first architecture decision in this project that spends money to
  buy latency. Approved by the owner on 2026-10-11 under CLAUDE.md §0.6.
- Provisioned concurrency is not covered by the budget guard aspect, so the cost sits in config and
  the budget alarm rather than in a synth-time check.
- A deactivated user who has **already** signed up never reaches this trigger; that case is the
  API's job (S1-06), and S1-03's acceptance criteria now say so.

**Cost:** about $1–3 per month, dev only until prod lands at M3.

## Validation

- S1-18: measured cold and warm p99 trigger duration on dev, recorded here.
- S1-03: uninvited, expired, already-accepted and deactivated emails are each refused with a stable
  error code; an invited email with a pending unexpired invitation passes.
- S1-17: negative test that `presignup_ro` cannot write.

## Partial dev measurement (2026-10-12, S1-18)

One hundred sequential, direct synthetic invocations of `presignup:live` version 3
produced the expected invited/refused decisions. One log tail contained initialization;
excluding it leaves 99 warm samples. Lambda REPORT durations were p50 **3.03 ms**,
p95 **43.17 ms**, and nearest-rank p99/max **54.37 ms**. The initialized observation's
reported invocation duration was 625.29 ms. Inputs used fixed fictional fixtures;
only case, version and durations were retained.

This is a warm synthetic admission measurement, not actual Google federation,
end-to-end Cognito latency or a cold-start p99 distribution. The actual Google
attempt exposed the provider-prefix case bug recorded in ADR-0008. Backend #28
subsequently released in successful execution `11d38ada-a392-471f-bf45-81592cedf9a5`,
and actual owner Google sign-in, repeat sign-in, logout and reload now pass.
S1-18 remains in progress until the remaining identity/denial evidence and
representative cold measurements are recorded. Memory stays at the approved 128 MiB
with reserved/provisioned concurrency 1; no resource settings changed for sampling.

## Cold-start evidence from existing dev logs (2026-10-13, S1-18)

The owner chose to record the existing CloudWatch evidence rather than run a separate
cold-start probe. A throwaway version could not be published without editing `$LATEST`
(publishing returned the existing version 5), and one direct invocation of `$LATEST` was
throttled with `ReservedFunctionConcurrentInvocationLimitExceeded`. With reserved and
provisioned concurrency both 1, the live alias has no on-demand path, so no on-demand cold
start can be measured, and none can occur: a second concurrent sign-up is throttled rather
than cold-started. No dev resource was changed.

Source: the `therapy-service-hours-dev-presignup` log group (32 log streams, one per execution
environment, versions 1 to 5, 2026-10-11T21:34Z to 2026-10-13T03:29Z), read once with a
`REPORT RequestId` / `INIT_REPORT` filter and no application log lines. Environments per
version: 4, 4, 15, 7 and 2. Inputs were the real deployed traffic and the earlier fixed
fictional synthetic fixtures; no identifiers are recorded here.

| Measure | n | Result |
|---|--:|---|
| `INIT_REPORT` init duration | 32 | min 1851.53 ms, median 2063.66 ms, p95 3057.93 ms, max 3064.99 ms |
| First invocation on a new environment | 7 | 45.12, 72.33, 73.73, 315.58, 332.91, 625.29, 1468.81 ms (median 315.58 ms) |
| Warm invocations | 113 | min 1.26 ms, median 3.04 ms, p95 46.95 ms, p99 71.82 ms, max 342.45 ms |

Twenty-six of the 32 init durations are 1.85 to 2.37 s. The six slowest are 2.60, 2.63, 2.81,
2.93, 3.06 and 3.06 s.

**Caveats.**

- **The sample is small.** With 32 init durations the nearest-rank p99 is the maximum, and with 7
  first-invocation samples no p99 is meaningful. This is not a p99 and is not recorded as one.
- **These are not on-demand cold starts.** None of the 120 `REPORT` lines carries an
  `Init Duration`, and 25 of the 32 environments never served a request. That is consistent
  with every initialisation being a proactive provisioned-concurrency (re)initialisation outside any
  Cognito request, but the log lines alone do not prove it. The 7 first invocations are the
  closest evidence of what Cognito sees on a fresh environment.
- **The headroom is thin.** Arithmetic, not a paired measurement: the worst init (3.06 s) plus the
  worst first invocation (1.47 s) is about 4.53 s, under the 5-second ceiling by about 0.47 s. That
  would matter only if an on-demand cold path ever existed, which the current concurrency settings
  prevent.
- **Not end to end.** The numbers cover Lambda initialisation and the first invocation including
  the database connection. They do not include Cognito's own time.

**Conclusion.** The evidence supports provisioned concurrency of 1 as the mitigation: the trigger
answered inside the budget on every recorded first invocation, and warm requests are far below it.
It does not establish a cold-start p99. If a representative on-demand p99 is wanted later, the
approach is a throwaway sibling function without reserved or provisioned concurrency (same code,
VPC, role and settings), one invocation per forced fresh environment, about 100 samples, deleted
afterwards. That would create a dev resource and was not run.

## Remaining open items

- Decide the exact memory size for the trigger, which sets the provisioned-concurrency cost.
- Revisit when prod is enabled at M3: two environments means two reservations.
