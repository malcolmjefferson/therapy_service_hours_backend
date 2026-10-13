-- S2-02: categories and clients (CLAUDE.md §7.4, §8). Static SQL only (SEC-20, SEC-21); every client
-- query is scoped to the owning user in the SQL itself (GO-47).

-- name: ListCategories :many
-- The 12 seeded categories in display order (group, then sort_order).
SELECT * FROM categories ORDER BY sort_order;

-- name: ListClients :many
SELECT * FROM clients
WHERE user_id = sqlc.arg(user_id)
ORDER BY lower(name), id;

-- name: GetClientByName :one
-- Case-insensitive, the same expression as clients_user_id_lower_name_idx.
SELECT * FROM clients
WHERE user_id = sqlc.arg(user_id) AND lower(name) = lower(sqlc.arg(name)::text);

-- name: InsertClient :one
-- A name that differs only by case violates clients_user_id_lower_name_idx (SQLSTATE 23505).
INSERT INTO clients (user_id, name)
VALUES (sqlc.arg(user_id), sqlc.arg(name))
RETURNING *;

-- S2-07: clients API. Every statement is scoped to the owning user in the SQL itself (GO-47), so
-- another user's client id matches no row and the caller sees 404.

-- name: ListClientsPage :many
SELECT * FROM clients
WHERE user_id = sqlc.arg(user_id)
ORDER BY lower(name), id
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountClients :one
SELECT count(*) FROM clients WHERE user_id = sqlc.arg(user_id);

-- name: LockClient :one
-- Locks the caller's client row. Attaching the client to an entry takes a KEY SHARE lock on it
-- (foreign key), so after this returns no entry can start using the client until the transaction ends.
SELECT * FROM clients
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: RenameClient :one
-- A name another client of the same user already has, ignoring case, violates
-- clients_user_id_lower_name_idx (SQLSTATE 23505). Changing only the case of the client's own name is allowed.
UPDATE clients SET name = sqlc.arg(name)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING *;

-- name: CountClientEntries :one
SELECT count(*) FROM entry_clients
WHERE client_id = sqlc.arg(client_id) AND user_id = sqlc.arg(user_id);

-- name: DeleteClient :execrows
-- entry_clients rows go with the client (ON DELETE CASCADE), which detaches it from its entries.
DELETE FROM clients WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);
