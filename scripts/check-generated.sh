#!/usr/bin/env bash
# Fails if generated code is stale: regenerates everything and checks git sees no change (ADR-0006, GO-49).
set -euo pipefail
cd "$(dirname "$0")/.."
go generate ./...
if [[ -n "$(git status --porcelain -- internal/http/gen)" ]]; then
  echo "Generated code is stale. Run 'go generate ./...' and commit the result:" >&2
  git status --short -- internal/http/gen >&2
  git --no-pager diff --stat -- internal/http/gen >&2
  exit 1
fi
echo "Generated code is up to date"
