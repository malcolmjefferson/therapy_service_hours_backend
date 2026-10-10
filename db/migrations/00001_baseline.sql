-- Baseline (S0-10): extensions the schema relies on. Forward-only (ADR-0003).
-- citext: case-insensitive emails (CLAUDE.md §9). btree_gist: the per-user overlap exclusion
-- constraint on entries (CLAUDE.md §9). Both are trusted extensions on RDS PostgreSQL 13+, so the
-- migrator role can create them without superuser rights.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS btree_gist;
