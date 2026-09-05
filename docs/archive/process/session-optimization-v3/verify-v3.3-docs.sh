#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCS="$ROOT/docs/会话优化v3"
PASS=0
FAIL=0

check_file() {
  local file="$1"
  if [[ -f "$DOCS/$file" ]]; then
    printf 'PASS file %s\n' "$file"
    PASS=$((PASS + 1))
  else
    printf 'FAIL file %s\n' "$file"
    FAIL=$((FAIL + 1))
  fi
}

check_text() {
  local file="$1"
  local pattern="$2"
  if rg -q --fixed-strings "$pattern" "$DOCS/$file"; then
    printf 'PASS text %s: %s\n' "$file" "$pattern"
    PASS=$((PASS + 1))
  else
    printf 'FAIL text %s: %s\n' "$file" "$pattern"
    FAIL=$((FAIL + 1))
  fi
}

for file in \
  "12-20260814-当前实现基线与缺口.md" \
  "13-V3.2实际API与SSE契约.md" \
  "14-统一逻辑点与任务拆解.md" \
  "15-检查、漂移与迁移门禁.md"; do
  check_file "$file"
done

check_text "12-20260814-当前实现基线与缺口.md" "V3.0 Semantic Track"
check_text "12-20260814-当前实现基线与缺口.md" "public.sessions/session_turns/session_bodies/session_turn_logs"
check_text "12-20260814-当前实现基线与缺口.md" "513_schema_unification_and_session_turns_dual_write"
check_text "13-V3.2实际API与SSE契约.md" "/api/admin/live-stream"
check_text "13-V3.2实际API与SSE契约.md" "/api/admin/providers/{id}/test-now"
check_text "13-V3.2实际API与SSE契约.md" "provider 级 test-now"
check_text "14-统一逻辑点与任务拆解.md" "V32-LP1"
check_text "14-统一逻辑点与任务拆解.md" "SEM-LP1"
check_text "15-检查、漂移与迁移门禁.md" "513_schema_unification"
check_text "15-检查、漂移与迁移门禁.md" "NOSUPERUSER"
check_text "15-检查、漂移与迁移门禁.md" "ui-verify-"

MIGRATION="$ROOT/sql/migrations/startup/513_schema_unification_and_session_turns_dual_write.sql"
if rg -q "DROP TABLE IF EXISTS public\.session_turns" "$MIGRATION"; then
  printf 'FAIL migration 513: still drops public.session_turns (should be gateway.*)\n'
  FAIL=$((FAIL + 1))
elif ! rg -q "DROP TABLE IF EXISTS gateway\.session_turns" "$MIGRATION"; then
  printf 'FAIL migration 513: missing gateway.session_turns DROP\n'
  FAIL=$((FAIL + 1))
else
  printf 'PASS migration 513 correctly drops gateway.* tables\n'
  PASS=$((PASS + 1))
fi

if rg -n "gateway\.(sessions|session_turns|session_bodies|session_turn_logs)" \
  "$DOCS/12-20260814-当前实现基线与缺口.md" \
  "$DOCS/13-V3.2实际API与SSE契约.md" \
  "$DOCS/14-统一逻辑点与任务拆解.md" \
  "$DOCS/15-检查、漂移与迁移门禁.md"; then
  printf 'FAIL current V3.3 docs still treat gateway.* as canonical\n'
  FAIL=$((FAIL + 1))
else
  printf 'PASS current V3.3 docs do not treat gateway.* as canonical\n'
  PASS=$((PASS + 1))
fi

printf '\nV3.3 docs: PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
if [[ "$FAIL" -ne 0 ]]; then
  exit 1
fi
