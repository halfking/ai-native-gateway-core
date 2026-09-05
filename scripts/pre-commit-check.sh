#!/usr/bin/env bash
# scripts/pre-commit-check.sh
# Local pre-commit gate for llm-gateway-go — catches the recurring "AI auto-fix"
# anti-pattern documented in docs/2026-06-21-three-day-audit.md §1.
#
# Runs the following checks before each commit. ANY failure => exit 1, commit blocked.
#
#   1. Vue type-check (cd web && npx vue-tsc --noEmit)  [web changes only]
#   2. SQL lint: forbid `$1` inside `SET LOCAL` statements (PG placeholder trap)
#   3. Migration numbering: every sql/migrations/startup/NNN_*.sql must have a unique NNN
#   4. go vet ./...  (cheap, runs in <2s)
#   5. Web token compliance: forbid P0 purple-family + var(--token, #hex) fallback [web changes only]
#
# Install (one-time per clone):
#   bash scripts/install-githooks.sh --pre-commit
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
  local mig_dir="sql/migrations/startup"
  if [[ ! -d "$mig_dir" ]]; then
    echo "$mig_dir not found; skipping"
    return 0
  fi
  local bad
  # The PostgreSQL trap: SET/SET LOCAL do not accept placeholders. AI agents
  # frequently write `SET LOCAL app.x = $1` and only find out at runtime.
  #
  # Skip lines starting with `--` (SQL comments) — historically 449_request_logs_hot_trace_events
  # documented the trap *as a comment* and tripped this check. Comments can't be
  # executed, so excluding them doesn't weaken the safety guarantee.
  bad=$(grep -rE 'SET[[:space:]]+(LOCAL[[:space:]]+)?[A-Za-z_][A-Za-z0-9_.]*[[:space:]]*=' \
        "$mig_dir"/ 2>/dev/null \
      | grep -v -E '^[^:]+:[[:space:]]*--' \
      | grep -E '\$[0-9]+' || true)
  if [[ -n "$bad" ]]; then
    echo "Found SET ... = \$N placeholders (PostgreSQL does not support placeholders in SET):"
    echo "$bad"
    return 1
  fi
  return 0
}

# ── 3. New migration numbering: unique NNN_*.sql ───────────────────────
check_migration_unique() {
  local mig_dir="sql/migrations/startup"
  if [[ ! -d "$mig_dir" ]]; then
    echo "$mig_dir not found; skipping"
    return 0
  fi
  # Existing historical migrations cannot be safely renamed because some
  # environments may already have recorded their filename. For each newly
  # staged forward migration, compare its version with the complete active
  # startup set so collisions with both existing and newly staged files fail.
  local new_files f base ver matches staged_files
  local -a conflicts=()
  new_files=$(git diff --cached --no-renames --name-only --diff-filter=A -- "$mig_dir" \
              | grep -E "^${mig_dir}/[0-9]{3}_.*\.sql$" \
              | grep -v -E '\.(down|disabled|fix)\.sql$' || true)
  staged_files=$(git ls-files --cached -- "$mig_dir" \
                 | grep -E "^${mig_dir}/[0-9]{3}_.*\.sql$" \
                 | grep -v -E '\.(down|disabled|fix)\.sql$' || true)
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    base=$(basename "$f")
    ver=${base%%_*}
    matches=$(printf '%s\n' "$staged_files" | grep -E "^${mig_dir}/${ver}_" | sort || true)
    if [[ $(printf '%s\n' "$matches" | grep -c .) -gt 1 ]]; then
      conflicts+=("$ver:$(printf '%s' "$matches" | xargs -n1 basename | paste -sd, -)")
    fi
  done <<<"$new_files"
  if [[ ${#conflicts[@]} -gt 0 ]]; then
    echo "Duplicate migration numbers in $mig_dir/:"
    printf '  %s\n' "${conflicts[@]}"
    echo "New forward migrations must not share a number with any active migration."
    return 1
  fi
  return 0
}

# ── 3b. Migration必须有对应的 down 脚本 (2026-07-09审计要求) ──────────
# 新增的SQL migration如果没有 down 脚本，回滚时会非常困难。
# 此检查扫描新增加（diff中为新增）的 *.sql 文件，确保每个都有 .down.sql 对应。
check_migration_has_down() {
  local mig_dirs=("sql/migrations/startup" "sql/migrations/domain")
  local missing=()

  for mig_dir in "${mig_dirs[@]}"; do
    if [[ ! -d "$mig_dir" ]]; then
      continue
    fi
    # 只检查新增的migration（git status 中显示为新增的）
    local new_files
    new_files=$(git status --porcelain 2>/dev/null \
                | grep -E '^\?\?.*\.sql$' \
                | awk '{print $2}' \
                | grep "^${mig_dir}/" \
                | grep -v '\.down\.sql$' || true)
    for f in $new_files; do
      local down_file
      if [[ "$f" == */up/*.sql ]]; then
        down_file="${f/\/up\//\/down\/}"
        down_file="${down_file%.sql}.down.sql"
      else
        down_file="${f%.sql}.down.sql"
      fi
      if [[ ! -f "$down_file" ]]; then
        missing+=("$f -> $down_file")
      fi
    done
  done

  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "❌ 新增 migration 缺少 down 脚本:"
    printf '   - %s\n' "${missing[@]}"
    echo ""
    echo "   修复: 为每个新增的 migration 创建对应的 .down.sql 文件"
    echo "   例: 364_xxx.sql -> 364_xxx.down.sql"
    echo ""
    echo "   参考: docs/migrations/migration-status-2026-07-09.md"
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

has_staged_web_changes() {
  git diff --cached --name-only -- 'web/**' | grep -q .
}

# ── 5. Web token compliance: forbid P0 purple-family + var() fallbacks ───
# 2026-07-22: rule 12 P0 forbids purple-family hex in UI accents. Without this
# check the 1145 var(--token, #hex) fallback patterns and ~150 direct purple
# hex usages that were cleaned up across 90+ files would silently regress.
# Only runs when web/ files have changes (to keep go-only commits fast).
check_web_token_compliance() {
  if [[ ! -d web/src ]]; then
    return 0
  fi
  local files
  # Staged + working-tree + untracked web/src changes; covers all commit paths.
  files=$( { git diff --cached --name-only -- 'web/src/**'; \
             git diff --name-only -- 'web/src/**'; \
             git ls-files --others --exclude-standard -- 'web/src/**'; } \
          | grep -E '\.(vue|ts|css)$' | sort -u)
  if [[ -z "$files" ]]; then
    return 0
  fi

  # Pattern 1: var(--token, #hex) fallback (was 1145 occurrences).
  local fallback_violations
  fallback_violations=$(grep -nE 'var\(--[a-z-]+,\s*#[0-9a-fA-F]+\)' $files 2>/dev/null \
                        | head -20 || true)
  if [[ -n "$fallback_violations" ]]; then
    echo "❌ var(--token, #hex) fallback patterns found (rule 12 P0):"
    echo "$fallback_violations" | sed 's/^/         /'
    echo "   CSS variables are defined in web/src/style.css; fallbacks are vestigial."
    echo "   Use bare var(--token) — fallbacks are never evaluated."
    return 1
  fi

  # Pattern 2: P0 purple-family hex (#667eea, #764ba2, #8b5cf6, #a78bfa,
  # #7c3aed, #6366f1, #5b21b6) outside of single/double-quoted strings.
  # Chart configs (ECharts) require static hex; chart files use quoted strings.
  # The quoted-string exclusion prevents false positives on those.
  local css_purple_hex
  css_purple_hex=$(grep -nE '#[0-9a-fA-F]{6}\b' $files 2>/dev/null \
                   | grep -iE '#(667eea|764ba2|8b5cf6|a78bfa|7c3aed|6366f1|5b21b6)\b' \
                   | grep -vE "['\"]#[0-9a-fA-F]+['\"]" \
                   | head -20 || true)
  if [[ -n "$css_purple_hex" ]]; then
    echo "❌ P0 purple-family hex found in CSS context (rule 12 §1):"
    echo "$css_purple_hex" | sed 's/^/         /'
    echo "   Replace with var(--accent) / var(--accent-h) / data-viz palette color."
    return 1
  fi

  # Pattern 3: P0 purple-family rgba() (e.g. rgba(99,102,241,...))
  local purple_rgba
  purple_rgba=$(grep -nE 'rgba\((99,?\s*102,?\s*241|139,?\s*92,?\s*246|67,?\s*56,?\s*202|168,?\s*85,?\s*247|124,?\s*58,?\s*237)' $files 2>/dev/null \
                | head -20 || true)
  if [[ -n "$purple_rgba" ]]; then
    echo "❌ P0 purple-family rgba() found (rule 12 §1):"
    echo "$purple_rgba" | sed 's/^/         /'
    echo "   Replace with color-mix(in srgb, var(--accent) X%, transparent)."
    return 1
  fi
  return 0
}

# ── runner ────────────────────────────────────────────────────────────
echo "pre-commit checks for llm-gateway-go"
echo "==================================="

run_check "go vet"                  check_go_vet
run_check "SQL: no SET+placeholder" check_sql_set_local
run_check "Migration: unique NNN"   check_migration_unique
run_check "Migration: has down.sql" check_migration_has_down
if has_staged_web_changes; then
  if [[ -d web/node_modules ]]; then
    run_vue_tsc
  else
    skip_check "Vue: vue-tsc" "web/node_modules not installed (cd web && npm ci to enable)"
  fi
  run_check "Web: token compliance" check_web_token_compliance
else
  skip_check "Vue: vue-tsc" "no staged web changes"
  skip_check "Web: token compliance" "no staged web changes"
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
