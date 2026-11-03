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

-- name: InsertImportedEntries :exec
-- Stores a whole file's accepted rows in one statement: one round trip instead of one per row, which
-- is what keeps a 5,000-row import inside the API Lambda's timeout (S4-04). The arrays are parallel,
-- one element per entry, and the ids are chosen by the caller so InsertImportedEntryClients can name
-- them. Every entry is the caller's own, pending, with needs_time derived from the start time exactly
-- as InsertEntry does. A session that overlaps another non-rejected one (a request that committed after
-- the service read the days) raises entries_no_overlap_excl (23P01) and nothing is stored. An empty
-- notes element means no notes.
INSERT INTO entries (id, user_id, session_date, start_time, end_time, duration_minutes, category_id, on_site, notes, needs_time)
SELECT r.id, sqlc.arg(user_id)::uuid, r.session_date, r.start_time, r.end_time, r.duration_minutes, r.category_id, r.on_site,
       NULLIF(r.notes, ''), r.start_time IS NULL
FROM (
    SELECT
        unnest(sqlc.arg(ids)::uuid[]) AS id,
        unnest(sqlc.arg(session_dates)::date[]) AS session_date,
        unnest(sqlc.arg(start_times)::time[]) AS start_time,
        unnest(sqlc.arg(end_times)::time[]) AS end_time,
        unnest(sqlc.arg(duration_minutes)::int[]) AS duration_minutes,
        unnest(sqlc.arg(category_ids)::uuid[]) AS category_id,
        unnest(sqlc.arg(on_sites)::boolean[]) AS on_site,
        unnest(sqlc.arg(notes)::text[]) AS notes
) AS r;

-- name: InsertImportedEntryClients :exec
-- Attaches clients to the entries InsertImportedEntries stored, one (entry, client) pair per element
-- of the parallel arrays. The composite foreign keys reject a client that belongs to another user
-- (23503); DISTINCT makes a repeated pair harmless.
INSERT INTO entry_clients (entry_id, client_id, user_id)
SELECT DISTINCT l.entry_id, l.client_id, sqlc.arg(user_id)::uuid
FROM (
    SELECT unnest(sqlc.arg(entry_ids)::uuid[]) AS entry_id, unnest(sqlc.arg(client_ids)::uuid[]) AS client_id
) AS l;
