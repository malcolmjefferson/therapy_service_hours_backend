-- S2-09: Idempotency-Key records. Static SQL only (SEC-20, SEC-21); every statement is scoped to the
-- owning user and key (GO-47). Time comes from the database clock so all Lambda instances agree.

-- name: ClaimIdempotencyKey :one
-- Takes the key for this request. Succeeds when no row exists, or when the row is expired, or when
-- it is an in-progress claim whose lease ran out (its request died). It returns no row when another
-- live request holds the key or has completed under it.
INSERT INTO idempotency_keys (user_id, key, request_hash, state, locked_until, expires_at)
VALUES (
    sqlc.arg(user_id), sqlc.arg(key), sqlc.arg(request_hash), 'in_progress',
    now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
    now() + make_interval(secs => sqlc.arg(ttl_seconds)::double precision)
)
ON CONFLICT (user_id, key) DO UPDATE SET
    request_hash = EXCLUDED.request_hash,
    state = 'in_progress',
    locked_until = EXCLUDED.locked_until,
    response_status = NULL,
    response_content_type = NULL,
    response_body = NULL,
    created_at = now(),
    expires_at = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= now()
   OR (idempotency_keys.state = 'in_progress' AND idempotency_keys.locked_until <= now())
RETURNING user_id;

-- name: GetIdempotencyKey :one
SELECT request_hash, state, locked_until, response_status, response_content_type, response_body
FROM idempotency_keys
WHERE user_id = sqlc.arg(user_id) AND key = sqlc.arg(key) AND expires_at > now();

-- name: CompleteIdempotencyKey :execrows
-- Only the request that holds the claim (same hash, still in progress) can complete it.
UPDATE idempotency_keys SET
    state = 'completed',
    response_status = sqlc.arg(response_status),
    response_content_type = sqlc.arg(response_content_type),
    response_body = sqlc.arg(response_body)
WHERE user_id = sqlc.arg(user_id) AND key = sqlc.arg(key)
  AND state = 'in_progress' AND request_hash = sqlc.arg(request_hash);

-- name: ReleaseIdempotencyKey :execrows
-- Frees a claim whose request did not succeed, so the client can retry the same key.
DELETE FROM idempotency_keys
WHERE user_id = sqlc.arg(user_id) AND key = sqlc.arg(key)
  AND state = 'in_progress' AND request_hash = sqlc.arg(request_hash);

-- name: PurgeExpiredIdempotencyKeys :execrows
-- The scheduled cleanup. Deletes at most batch_size rows so one run stays short; it is called
-- again until it deletes fewer.
DELETE FROM idempotency_keys
WHERE (user_id, key) IN (
    SELECT k.user_id, k.key FROM idempotency_keys k
    WHERE k.expires_at <= now()
    ORDER BY k.expires_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
);
