-- S4-03: Markdown import (CLAUDE.md §7.7, FR-IO-02, FR-IO-03). Static SQL only (SEC-20, SEC-21); every
-- statement carries the owner's user_id (GO-47). The import service runs these in one transaction.

-- name: LockUserImport :exec
-- Serializes one user's imports for the rest of the transaction, so two imports of the same file made
-- at the same time cannot both find no duplicates. The key is the user's id; the lock is released at
-- commit or rollback. Other users are unaffected.
SELECT pg_advisory_xact_lock(hashtextextended('import-entries:' || sqlc.arg(user_id)::text, 0));

-- name: InsertClientIfAbsent :one
-- Saves a client unless the user already has one with that name ignoring case. No row comes back for
-- an existing name (pgx.ErrNoRows); the caller then reads it with GetClientByName. Unlike InsertClient
-- this never raises a unique violation, which would abort the whole import transaction. Concurrent
-- inserts of one name wait for each other, and the loser's GetClientByName sees the winner.
INSERT INTO clients (user_id, name)
VALUES (sqlc.arg(user_id), sqlc.arg(name))
ON CONFLICT (user_id, lower(name)) DO NOTHING
RETURNING *;

-- name: ListEntriesForImport :many
-- What the import compares each row with: the user's entries between two dates, every status, with
-- the ids of their clients. Duplicates are found among all statuses; overlaps ignore rejected entries
-- and entries without times in the service, as entries_no_overlap_excl does.
SELECT
    e.id,
    e.session_date,
    e.start_time,
    e.end_time,
    e.duration_minutes,
    e.category_id,
    e.notes,
    e.status,
    COALESCE(array_agg(ec.client_id ORDER BY ec.client_id) FILTER (WHERE ec.client_id IS NOT NULL), '{}')::uuid[] AS client_ids
FROM entries e
LEFT JOIN entry_clients ec ON ec.entry_id = e.id AND ec.user_id = e.user_id
WHERE e.user_id = sqlc.arg(user_id)
  AND e.session_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
GROUP BY e.id
ORDER BY e.session_date, e.start_time NULLS FIRST, e.id;
