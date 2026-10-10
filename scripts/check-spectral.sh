#!/usr/bin/env bash
# Lints api/openapi.yaml with the repo ruleset, then proves the ruleset itself works: the valid
# fixture must pass and each api/testdata/spectral/<rule>.yaml must fail with exactly that rule.
set -euo pipefail
cd "$(dirname "$0")/.."

SPECTRAL_VERSION="6.17.0"
spectral() { npx -y "@stoplight/spectral-cli@${SPECTRAL_VERSION}" lint --ruleset .spectral.yaml --fail-severity=warn "$@"; }

echo "== api/openapi.yaml"
spectral api/openapi.yaml

echo "== fixture: valid"
spectral api/testdata/spectral/valid.yaml >/dev/null

failed=0
for fixture in api/testdata/spectral/tsh-*.yaml; do
  rule="$(basename "$fixture" .yaml)"
  if output="$(spectral "$fixture" 2>&1)"; then
    echo "FAIL: $rule fixture passed lint" >&2; failed=1
  elif ! grep -q "$rule" <<<"$output"; then
    echo "FAIL: $rule fixture failed for another reason:" >&2; echo "$output" >&2; failed=1
  else
    echo "ok: $rule"
  fi
done
exit "$failed"
