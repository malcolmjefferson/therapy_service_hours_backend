#!/usr/bin/env bash
# Fails when api/openapi.yaml breaks clients compared with a base spec (ADR-0006, ARC-14).
#   scripts/check-breaking.sh <base-spec-file>   compare against a base spec
#   scripts/check-breaking.sh --self-test        prove a removed endpoint is caught
# A PR labelled `breaking-change` skips this check in CI; both sides must then ship together.
set -euo pipefail
cd "$(dirname "$0")/.."

breaking() { go tool oasdiff breaking "$1" "$2" --fail-on ERR --format text; }

if [[ "${1:-}" == "--self-test" ]]; then
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  sed 's#^  /healthz:#  /healthz-removed:#' api/openapi.yaml > "$tmp/broken.yaml"
  breaking api/openapi.yaml api/openapi.yaml >/dev/null
  echo "ok: an unchanged spec passes"
  if breaking api/openapi.yaml "$tmp/broken.yaml" >/dev/null 2>&1; then
    echo "FAIL: removing /healthz was not reported as breaking" >&2; exit 1
  fi
  echo "ok: removing an endpoint is reported as breaking"
  exit 0
fi

base="${1:?usage: check-breaking.sh <base-spec-file> | --self-test}"
breaking "$base" api/openapi.yaml
echo "No breaking changes against $base"
