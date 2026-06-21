#!/usr/bin/env bash
# scripts/pre-commit-check.sh
# Local pre-commit gate for llm-gateway-go — catches the recurring "AI auto-fix"
# anti-pattern documented in docs/2026-06-21-three-day-audit.md §1.
#
# Runs four checks before each commit. ANY failure => exit 1, commit blocked.
#
#   1. Vue type-check (cd web && npx vue-tsc --noEmit)
#   2. SQL lint: forbid `$1` inside `SET LOCAL` statements (PG placeholder trap)
#   3. Migration numbering: every db/migrations/NNN_*.sql must have a unique NNN
#   4. go vet ./...  (cheap, runs in <2s)
#
# Install (one-time per clone):
#   bash scripts/pre-commit-install.sh
# Or wire to your own git hook runner.
#
# Bypass (NOT recommended):
#   git commit --no-verify
#
# Exit codes:
#   0  all checks passed
#   1  one or more checks failed
#   2  required tool missing (vue-tsc / psql / go)

set -euo pipefail

# Resolve repo root robustly. When invoked as `bash scripts/pre-commit-check.sh`
# BASH_SOURCE is unreliable (especially under set -u), so use $0 + realpath.
SCRIPT_PATH="$(realpath "${BASH_SOURCE[0]:-$0}" 2>/dev/null || echo "$0")"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
# scripts/ is one level under REPO_ROOT (this is a Go service, not a monorepo
# deploy script that lives two levels deep). scripts/.. == REPO_ROOT.
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
SKIP=0
WARN=0
declare -a FAILURES=()

run_check() {
  local name="$1"; shift
  printf "  [%s] " "$name"
  # Capture stdout/stderr so we can show the first lines of failure output
  # without losing exit code. run_check returns 0 always (failures are
  # counted, not propagated), so the gate reports everything in one go.
  local out rc
  if out=$("$@" 2>&1); then
    rc=0
  else
    rc=$?
  fi
  if [[ $rc -eq 0 ]]; then
    if [[ -n "$out" ]]; then
      echo "PASS"
      echo "$out" | sed 's/^/         /'
    else
      echo "PASS"
    fi
    PASS=$((PASS + 1))
  else
    echo "FAIL (exit $rc)"
    FAIL=$((FAIL + 1))
    FAILURES+=("$name")
    if [[ -n "$out" ]]; then
      echo "$out" | head -10 | sed 's/^/         /'
    fi
  fi
}

skip_check() {
  local name="$1"; local reason="$2"
  printf "  [%s] SKIP — %s\n" "$name" "$reason"
  SKIP=$((SKIP + 1))
}

# run_vue_tsc: vue-tsc is now a hard gate (see check_vue_tsc body).
# Reports PASS or FAIL but never WARN. Kept separate so the run_check
# function does not need to know about the FAIL semantic.
run_vue_tsc() {
  printf "  [Vue: vue-tsc] "
  local vout
  if vout=$(check_vue_tsc 2>&1); then
    echo "PASS"
    PASS=$((PASS + 1))
  else
    echo "FAIL"
    FAIL=$((FAIL + 1))
    FAILURES+=("Vue: vue-tsc")
  fi
  if [[ -n "$vout" ]]; then
    echo "$vout" | sed 's/^/         /'
  fi
}

# ── 1. go vet ─────────────────────────────────────────────────────────
check_go_vet() {
  if ! command -v go >/dev/null 2>&1; then
    echo "go not installed"
    return 2
  fi
  go vet ./... >/dev/null
}

# ── 2. SQL lint: ban `$1` in SET LOCAL ─────────────────────────────────
check_sql_set_local() {
  if [[ ! -d db/migrations ]]; then
    echo "db/migrations not found; skipping"
    return 0
  fi
  local bad
  # The PostgreSQL trap: SET/SET LOCAL do not accept placeholders. AI agents
  # frequently write `SET LOCAL app.x = $1` and only find out at runtime.
  bad=$(grep -rE 'SET[[:space:]]+(LOCAL[[:space:]]+)?[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*=' \
        db/migrations/ 2>/dev/null \
      | grep -E '\$[0-9]+' || true)
  if [[ -n "$bad" ]]; then
    echo "Found SET ... = \$N placeholders (PostgreSQL does not support placeholders in SET):"
    echo "$bad"
    return 1
  fi
  return 0
}

# ── 3. Migration numbering: unique NNN_*.sql ───────────────────────────
check_migration_unique() {
  if [[ ! -d db/migrations ]]; then
    echo "db/migrations not found; skipping"
    return 0
  fi
  # NNN_*.sql and NNN_*.down.sql are a pair; only the .sql counts as the
  # forward migration. Otherwise 020_unique_request_id and
  # 020_unique_request_id.down.sql would falsely collide.
  local dups
  dups=$(ls db/migrations/ 2>/dev/null \
         | grep -E '^[0-9]{3}_' \
         | grep -v '\.down\.sql$' \
         | sed -E 's/^([0-9]{3})_.*/\1/' \
         | sort | uniq -d)
  if [[ -n "$dups" ]]; then
    echo "Duplicate migration numbers in db/migrations/: $dups"
    echo "Round-48 audit found 024/025 collisions; this guard prevents recurrence."
    return 1
  fi
  return 0
}

# ── 4. Vue type-check ──────────────────────────────────────────────────
check_vue_tsc() {
  if [[ ! -d web ]]; then
    echo "web/ not found; skipping"
    return 0
  fi
  if ! command -v npx >/dev/null 2>&1; then
    echo "npx not installed; skipping"
    return 0
  fi
  if [[ ! -d web/node_modules ]]; then
    echo "web/node_modules not installed (cd web && npm ci to enable); skipping"
    return 0
  fi
  # vue-tsc: HARD GATE.
  #
  # 2026-06-22: 62 pre-existing vue-tsc errors (v6.0 audit) were
  # all fixed in commits 8c2c6d57 through 7c923c5c. The hook is
  # now a hard fail when new errors creep in. Last batch also fixed
  # the ClientConfigDialog safety bug (ApiKey object passed where
  # string was expected, causing config files to ship a giant
  # metadata blob instead of a real API key).
  local out
  if out=$(cd web && npx vue-tsc --noEmit 2>&1); then
    return 0
  fi
  local rc=$?
  local err_count
  err_count=$(echo "$out" | grep -cE 'error TS[0-9]+' || echo 0)
  echo "FAIL: vue-tsc found $err_count TypeScript error(s) (full list: cd web && npx vue-tsc --noEmit):"
  echo "$out" | grep -E 'error TS[0-9]+' | head -10 | sed 's/^/         /'
  return 1
}

# ── runner ────────────────────────────────────────────────────────────
echo "pre-commit checks for llm-gateway-go"
echo "==================================="

run_check "go vet"                  check_go_vet
run_check "SQL: no SET+placeholder" check_sql_set_local
run_check "Migration: unique NNN"   check_migration_unique
if [[ -d web/node_modules ]]; then
  # vue-tsc is a WARN-only check; failures are reported but do not block.
  run_vue_tsc
else
  skip_check "Vue: vue-tsc" "web/node_modules not installed (cd web && npm ci to enable)"
fi

echo "==================================="
echo "PASS=$PASS FAIL=$FAIL WARN=$WARN SKIP=$SKIP"
if [[ $FAIL -gt 0 ]]; then
  echo ""
  echo "FAILED CHECKS:"
  for f in "${FAILURES[@]}"; do
    echo "  - $f"
  done
  echo ""
  echo "Bypass: git commit --no-verify  (NOT recommended)"
  exit 1
fi
echo "all checks passed"
