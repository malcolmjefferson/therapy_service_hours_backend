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
