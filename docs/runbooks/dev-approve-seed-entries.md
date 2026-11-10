# Dev-only approved-hours seed (S3-07)

## Purpose and boundary

Until the review workflow (S7) exists, nothing can approve an entry, so goal progress stays at zero in
end-to-end tests. The IAM-only seed Lambda accepts a separate `approve-entries` operation that marks the
**pending** entries of one synthetic test user **approved** so the goal cards can show approved hours.

- **Dev only.** The service refuses it unless `APP_ENV=dev`; production refuses it regardless of any flag.
- **Synthetic accounts only.** The email must end in `@example.invalid` (the ADR-0011 test users). A real
  person's entries can never be changed through it.
- **No HTTP route, no new IAM grant, no new resource.** It is reachable only by `lambda:InvokeFunction` on
  `therapy-service-hours-dev-seed`, like the other seed operations.
- Rejected and already approved entries are left alone; a repeat run approves nothing more.
- The response is a count only: `{"status":"ok","approved":{"entries_approved":2}}`.

## Event

```json
{"operation": "approve-entries", "approve_email": "test-invited@example.invalid"}
```

Mixing it with seed or verification fields (`admin_email`, `allow_prod`, `include_dev_users`,
`verification_email`, `expected_subject`) is refused with `INVALID_SEED_REQUEST` before storage.

## Verifying

`go test ./internal/service/seed ./internal/seed ./internal/migrate` covers the guards, the dispatch and the
real SQL against PostgreSQL.
