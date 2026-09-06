#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SQL="$ROOT_DIR/sql/diagnostics/selfcheck_diagnostics.sql"
SCRIPT="$ROOT_DIR/scripts/diagnose_selfcheck.sh"

fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}
pass() {
  printf 'PASS %s\n' "$*"
}

[[ -f "$SQL" ]] || fail "missing diagnostics SQL: $SQL"
[[ -f "$SCRIPT" ]] || fail "missing diagnostics script: $SCRIPT"
bash -n "$SCRIPT" || fail "diagnostics script has shell syntax errors"

if grep -nE '(^|[^[:alnum:]_])providers\.name([^[:alnum:]_]|$)' "$SQL" "$SCRIPT"; then
  fail "diagnostics still references nonexistent providers.name"
fi

grep -q 'pv\.display_name AS provider_name' "$SQL" || fail "SQL does not use providers.display_name"
grep -q 'pv\.display_name AS provider_name' "$SCRIPT" || fail "shell diagnostics does not use providers.display_name"
grep -q 'GROUP BY c\.id, c\.label, pv\.display_name' "$SCRIPT" || fail "shell diagnostics GROUP BY is not schema-correct"
grep -q 'mo\.credential_id IS NULL' "$SQL" || fail "missing model_offers missing-row check"
grep -q 'cmb\.available IS DISTINCT FROM mo\.available' "$SQL" || fail "missing NULL-safe availability comparison"
grep -q 'cmb\.unavailable_reason IS DISTINCT FROM mo\.unavailable_reason' "$SQL" || fail "missing NULL-safe reason comparison"

pass 'self-check diagnostics schema and NULL-safety contract passed'
