#!/usr/bin/env bash
# Fails if sqlc-generated code differs from what db/queries would generate (GO-49, SEC-22).
# S1 identity queries introduce sqlc.yaml; the generator is pinned as a Go tool.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ ! -f sqlc.yaml ]]; then
  echo "::notice::No sqlc.yaml yet; the sqlc staleness check starts with the first query."
  exit 0
fi
go tool sqlc diff
echo "sqlc output is current"
