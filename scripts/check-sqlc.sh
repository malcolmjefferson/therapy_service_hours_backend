#!/usr/bin/env bash
# Fails if sqlc-generated code differs from what db/queries would generate (GO-49, SEC-22).
# Until the first query exists (S2), there is no sqlc.yaml and the check passes with a notice.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ ! -f sqlc.yaml ]]; then
  echo "::notice::No sqlc.yaml yet; the sqlc staleness check starts with the first query."
  exit 0
fi
go tool sqlc diff
echo "sqlc output is current"
