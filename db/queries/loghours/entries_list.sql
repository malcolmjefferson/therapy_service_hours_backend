-- S2-02: the entries table (CLAUDE.md §7.1, §11.1; FR-LOG-08). GENERATED TEXT, COMMITTED SQL.
--
-- One fixed query per allowed (sort, direction) pair, because ORDER BY cannot be a bound parameter
-- (SEC-24, GO-14). Every query has the same filters and the same page window; only ORDER BY differs.
-- The Go allowlist in internal/db/loghoursqueries/list.go maps a sort key and direction to one of them,
-- and a test checks that all ten return the same rows. Add a sortable column by adding two queries,
-- one allowlist entry, and a case in that test.
--
-- Filters are optional named parameters in the SQL itself (GO-44): NULL means "no filter".
--   category_group  direct | indirect | supervision
--   from_date/to_date  inclusive session_date bounds
--   client_text  case-insensitive substring of a client name; \, % and _ are escaped here with an
--                explicit ESCAPE (SEC-25), so they match literally. The caller caps the length at 64.
--
-- Pages are stable: every ORDER BY ends in a unique key, e.id (legacy entries without times sort last
-- within their date). The ten statements return the Entry model; clients come from ListEntryClients and
-- category names from ListCategories, so a page costs three queries however many rows it holds.
-- Category order is group then name; Client order is the entry's client names, alphabetically joined,
-- with entries that have no client last in both directions.

-- name: ListEntriesDateAsc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.session_date ASC, e.start_time ASC NULLS LAST, e.id ASC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesDateDesc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesCategoryAsc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY c.category_group ASC, c.name ASC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesCategoryDesc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY c.category_group DESC, c.name DESC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesClientAsc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY (SELECT string_agg(lower(cl2.name), '; ' ORDER BY lower(cl2.name), cl2.id)
          FROM entry_clients ec2 JOIN clients cl2 ON cl2.id = ec2.client_id AND cl2.user_id = ec2.user_id
          WHERE ec2.entry_id = e.id AND ec2.user_id = e.user_id) ASC NULLS LAST, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesClientDesc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY (SELECT string_agg(lower(cl2.name), '; ' ORDER BY lower(cl2.name), cl2.id)
          FROM entry_clients ec2 JOIN clients cl2 ON cl2.id = ec2.client_id AND cl2.user_id = ec2.user_id
          WHERE ec2.entry_id = e.id AND ec2.user_id = e.user_id) DESC NULLS LAST, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesHoursAsc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.duration_minutes ASC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesHoursDesc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.duration_minutes DESC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesOnSiteAsc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.on_site ASC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: ListEntriesOnSiteDesc :many
SELECT e.*
FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ))
ORDER BY e.on_site DESC, e.session_date DESC, e.start_time DESC NULLS LAST, e.id DESC
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::int;

-- name: CountEntries :one
-- Total rows for the same filters, for pagination.
SELECT count(*) FROM entries e
JOIN categories c ON c.id = e.category_id
WHERE e.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(category_group)::text IS NULL OR c.category_group = sqlc.narg(category_group)::text)
  AND (sqlc.narg(from_date)::date IS NULL OR e.session_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR e.session_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(client_text)::text IS NULL OR EXISTS (
        SELECT 1 FROM entry_clients ec
        JOIN clients cl ON cl.id = ec.client_id AND cl.user_id = ec.user_id
        WHERE ec.entry_id = e.id AND ec.user_id = e.user_id
          AND cl.name ILIKE '%' || replace(replace(replace(sqlc.narg(client_text)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  ));
