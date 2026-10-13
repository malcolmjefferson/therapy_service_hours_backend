# Read-only dev owner binding verification (S1-18)

## Purpose and boundary

After the real invited owner has signed in and signed out/in again, prove that
one Google identity maps to one active database admin row and one accepted
invitation. The existing IAM-only seed Lambda accepts a separate
`verify-binding` operation. Its database transaction is read-only; it does not
run the seed plan, bind a subject, accept/renew an invitation or restore roles.
Production refuses this operation regardless of the normal seed opt-in flag.
There is no HTTP route, new IAM grant, resource, dependency or auth bypass.

The event uses `verification_email`, not `admin_email`. Older seed binaries do
not recognize the new operation and see no required admin email, so they refuse
before storage. Mixed seed/verification fields and unknown operations are also
refused. The response contains counts only; zero/mismatched counts must be
investigated rather than repaired by the verification tool.

## Operator procedure

1. Merge and successfully release the backend verification change to dev.
2. Use the existing authorized operator AWS session in the intended account.
   Required existing permissions are `sts:GetCallerIdentity`,
   `cognito-idp:DescribeUserPool`, `cognito-idp:ListUsers` on the dev pool and
   `lambda:InvokeFunction` on `therapy-service-hours-dev-seed`. This helper does
   not assume or expand the pipeline's smoke-reader role. An unauthorized
   session fails closed; use the authorized operator session.
3. Set `APP_ENV=dev`, `EXPECTED_AWS_ACCOUNT` to the expected account,
   `USER_POOL_ID` to the deployed dev pool ID and `ADMIN_EMAIL` to the intended
   owner identity. Supply values locally; do not put them in public tickets or
   commit an environment file. The helper checks account, region, pool name,
   exactly one enabled federated Google identity and verified email.
4. From the backend repository, run:

   ```powershell
   $env:PYTHONDONTWRITEBYTECODE = '1'
   python scripts/verify_dev_binding.py
   ```

   Bash: `PYTHONDONTWRITEBYTECODE=1 python3 scripts/verify_dev_binding.py`.
   The CLI captures Cognito attributes and the subject in memory and sends the
   Lambda input through private temporary files, never process arguments.
   Files are removed on success and failure; raw CLI/JSON errors are redacted.
5. Success requires `google_identities=1` and **exactly one** of each binding
   count: `users`, `subject_users`, `matching_active_admin`,
   `accepted_invitations`. The last count checks the same user's organization
   and a non-null acceptance stamp. Repeat after another real Google sign-in;
   both count-only results must remain unchanged. Retain safe results and
   release references, never email/subject/token material.

An accepted-invitation count above one can reflect prior invitation history;
the tool deliberately does not claim the clean first-sign-in proof in that case.
A deactivated/demoted user or a different subject cannot pass the active-admin
match. Do not re-invite or change roles simply to make this check green.

## Validation and remaining acceptance

PostgreSQL tests exercise pending, bound/repeated, wrong subject/email, demoted
and deactivated states and compare complete users/invitations snapshots before
and after every verification. Unit tests cover prod refusal before storage,
input validation, legacy seed compatibility, old-binary fail-closed decoding,
dispatch isolation and response/log privacy. Python tests cover operator target
guards, count-only output, private inputs, cleanup and raw-error redaction.

```bash
TESTCONTAINERS_RYUK_DISABLED=true go test -count=1 -v ./internal/migrate -run TestOperatorBindingVerificationIsReadOnlyAcrossIdentityStates
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p test_verify_dev_binding.py
```

This prepares reproducible binding evidence. Until released and run twice on
dev, the actual persisted owner binding proof remains open. It does not prove
representative cold-start p99 or real uninvited/deactivated Google acceptance.
