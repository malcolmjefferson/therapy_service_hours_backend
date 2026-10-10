#!/usr/bin/env bash
# Rejects destructive SQL in migrations unless the file is tagged `-- +contract` (ADR-0003, GO-50).
#   scripts/check-migrations.sh [dir]   check a directory (default db/migrations)
#   scripts/check-migrations.sh --self-test
set -euo pipefail
cd "$(dirname "$0")/.."

# DROP of any object, ALTER ... DROP (column/constraint), TRUNCATE and RENAME all break code that
# is still running during an expand/contract release.
DESTRUCTIVE='\b(drop|truncate|rename)\b'

check_dir() {
  local dir="$1" failed=0 hits
  for f in "$dir"/*.sql; do
    [[ -e "$f" ]] || continue
    if grep -qE '^--[[:space:]]*\+contract\b' "$f"; then
      continue
    fi
    # Ignore full-line comments, then look for destructive keywords (case-insensitive).
    if hits="$(grep -nvE '^[[:space:]]*--' "$f" | grep -iE "$DESTRUCTIVE")"; then
      echo "$f: destructive statement in a migration not tagged '-- +contract' (ADR-0003):" >&2
      echo "$hits" | sed 's/^/  line /' >&2
      failed=1
    fi
  done
  return "$failed"
}

if [[ "${1:-}" == "--self-test" ]]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir "$tmp/bad" "$tmp/good"
  printf -- '-- +goose Up\nALTER TABLE entries DROP COLUMN notes;\n' > "$tmp/bad/00002_drop.sql"
  printf -- '-- +goose Up\n-- +contract\nALTER TABLE entries DROP COLUMN legacy;\n' > "$tmp/good/00002_contract.sql"
  printf -- '-- Never drop data here.\n-- +goose Up\nCREATE TABLE t (id int);\n' > "$tmp/good/00003_comment.sql"
  if check_dir "$tmp/bad" 2>/dev/null; then
    echo "FAIL: an untagged DROP COLUMN passed" >&2
    exit 1
  fi
  echo "ok: an untagged DROP COLUMN is rejected"
  check_dir "$tmp/good"
  echo "ok: tagged contract migrations and comments pass"
  exit 0
fi

check_dir "${1:-db/migrations}"
echo "Migrations are additive (or explicitly tagged -- +contract)"
