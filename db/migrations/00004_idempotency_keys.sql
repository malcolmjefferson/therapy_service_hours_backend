-- S2-09: Idempotency-Key records (GO-57, ARC-17). Additive and forward-only (ADR-0003).
-- One row per (user, key): the claim made before a create runs and, once it succeeds, the response
-- to replay. The store lives in PostgreSQL, so no extra service is needed.

-- +goose Up
CREATE TABLE idempotency_keys (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Client-generated, one per user action (FE-34). Scoped to the user, so another user's key never collides.
    key uuid NOT NULL,
    -- SHA-256 of the method, route and canonical body. A different hash under the same key is a 409.
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    state text NOT NULL CHECK (state IN ('in_progress', 'completed')),
    -- While in_progress, the claim belongs to the request that made it until this time. A request that
    -- died (Lambda timeout) therefore cannot block its key until the TTL passes.
    locked_until timestamptz NOT NULL,
    response_status smallint,
    response_content_type text CHECK (char_length(response_content_type) <= 200),
    response_body bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- After this time the key is free again and the scheduled cleanup may delete the row.
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, key),
    -- A completed row carries a replayable response; an in-progress row carries none.
    CONSTRAINT idempotency_keys_response_chk CHECK (
        (state = 'completed') = (response_status IS NOT NULL AND response_content_type IS NOT NULL AND response_body IS NOT NULL)
    ),
    CONSTRAINT idempotency_keys_status_chk CHECK (response_status IS NULL OR response_status BETWEEN 200 AND 299),
    CONSTRAINT idempotency_keys_expiry_chk CHECK (expires_at > created_at)
);
-- The scheduled cleanup deletes by expiry.
CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);
