-- S3-02: index for the goal-progress aggregate (CLAUDE.md §7.2, FR-GOAL-03). Additive and forward-only (ADR-0003).
-- The progress query reads one user's approved and pending entries and sums their minutes by category
-- group, category and on-site flag. (user_id, status) finds exactly those rows; the INCLUDE columns are
-- the only other entries columns the query reads, so PostgreSQL can answer from the index alone on a
-- vacuumed table instead of visiting every entry. Rejected entries are never read.

-- +goose Up
CREATE INDEX entries_user_id_status_idx ON entries (user_id, status) INCLUDE (category_id, on_site, duration_minutes);
