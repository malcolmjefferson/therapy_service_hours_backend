-- S2-02: writes and single-row reads on entries (CLAUDE.md §7.1). Static SQL only (SEC-20, SEC-21).
-- Every statement carries the owner's user_id (GO-47). Zero rows means "not yours or not found"; the
-- caller answers 404 and never says which (SEC-12).

-- name: GetEntry :one
SELECT * FROM entries
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: InsertEntry :one
-- needs_time is derived from the start time so it can never disagree with entries_needs_time_chk.
-- Overlaps are rejected by entries_no_overlap_excl (SQLSTATE 23P01).
INSERT INTO entries (
    user_id, session_date, start_time, end_time, duration_minutes, category_id, on_site, notes, needs_time
) VALUES (
    sqlc.arg(user_id), sqlc.arg(session_date)::date, sqlc.narg(start_time)::time, sqlc.narg(end_time)::time,
    sqlc.arg(duration_minutes)::int, sqlc.arg(category_id), sqlc.arg(on_site)::boolean, sqlc.narg(notes)::text,
    sqlc.narg(start_time)::time IS NULL
)
RETURNING *;

-- name: UpdateEntry :one
-- Only the owner's pending or rejected entries change; approved entries are locked (CLAUDE.md §7.1).
-- Saving an edited rejected entry sets it back to pending. The review columns are left as they are:
-- the rejection reason is not copied anywhere else until the S7 status history exists.
UPDATE entries SET
    session_date = sqlc.arg(session_date)::date,
    start_time = sqlc.narg(start_time)::time,
    end_time = sqlc.narg(end_time)::time,
    duration_minutes = sqlc.arg(duration_minutes)::int,
    category_id = sqlc.arg(category_id),
    on_site = sqlc.arg(on_site)::boolean,
    notes = sqlc.narg(notes)::text,
    needs_time = sqlc.narg(start_time)::time IS NULL,
    status = 'pending',
    updated_at = now()
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status IN ('pending', 'rejected')
RETURNING *;

-- name: DeleteEntry :execrows
-- Same ownership and status rule as UpdateEntry; entry_clients rows go with the entry (ON DELETE CASCADE).
DELETE FROM entries
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND status IN ('pending', 'rejected');

-- name: FindOverlappingEntry :one
-- The entry that entries_no_overlap_excl would collide with, so a 409 can name it. The test is the
-- constraint's: same user and date, rejected and legacy entries ignored, half-open ranges (back-to-back
-- sessions do not overlap). exclude_id leaves the entry being edited out of the search.
SELECT id, session_date, start_time, end_time FROM entries
WHERE user_id = sqlc.arg(user_id)
  AND session_date = sqlc.arg(session_date)::date
  AND status <> 'rejected'
  AND start_time IS NOT NULL
  AND start_time < sqlc.arg(end_time)::time
  AND end_time > sqlc.arg(start_time)::time
  AND (sqlc.narg(exclude_id)::uuid IS NULL OR id <> sqlc.narg(exclude_id)::uuid)
ORDER BY start_time, id
LIMIT 1;

-- name: DeleteEntryClients :exec
DELETE FROM entry_clients
WHERE entry_id = sqlc.arg(entry_id) AND user_id = sqlc.arg(user_id);

-- name: InsertEntryClients :exec
-- Attaches a set of clients in one statement. The composite foreign keys reject a client that belongs
-- to another user (SQLSTATE 23503); DISTINCT makes a repeated id harmless.
INSERT INTO entry_clients (entry_id, client_id, user_id)
SELECT sqlc.arg(entry_id)::uuid, ids.client_id, sqlc.arg(user_id)::uuid
FROM (SELECT DISTINCT unnest(sqlc.arg(client_ids)::uuid[]) AS client_id) AS ids;

-- name: ListEntryClients :many
-- The clients of a page of entries, one client per row, in the order the table shows them.
SELECT ec.entry_id, cl.id AS client_id, cl.name
FROM entry_clients ec
JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
WHERE ec.user_id = sqlc.arg(user_id) AND ec.entry_id = ANY(sqlc.arg(entry_ids)::uuid[])
ORDER BY ec.entry_id, lower(cl.name), cl.id;
