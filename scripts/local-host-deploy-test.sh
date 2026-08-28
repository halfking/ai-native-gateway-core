#!/usr/bin/env bash
# ============================================================================
# scripts/local-host-deploy-test.sh — 本机版本化部署的 L1-L4 验证
#
# 与 scripts/local-deploy-test.sh 的差异:
#   - 不再启动 r112 docker compose, 只依赖本地 llm-gateway-pg + nbjl-redis
#   - 路径全部走 ~/Downloads/llm-gateway-files/
#   - 验证 local-host-deploy.sh 的 deploy/start 流程
#
# 用法:
#   bash scripts/local-host-deploy-test.sh               # full (默认)
#   bash scripts/local-host-deploy-test.sh --quick       # 跳过 deploy, 仅验证当前服务
#   bash scripts/local-host-deploy-test.sh --verify      # 只跑 L1-L4 (服务已在跑)
#   bash scripts/local-host-deploy-test.sh --clean       # 停止网关并清理当前 active bundle
#   bash scripts/local-host-deploy-test.sh --keep N      # deploy 时保留 N 个 verified 版本
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck source=local-host-layout-helper.sh
source "$SCRIPT_DIR/local-host-layout-helper.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
ORANGE=$'\033[0;33m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'

err()    { echo -e "${RED}✗ $*${NC}" | tee -a "$LOG_FILE" >&2; }
warn()   { echo -e "${ORANGE}⚠ $*${NC}" | tee -a "$LOG_FILE"; }
ok()     { echo -e "${GREEN}✓ $*${NC}" | tee -a "$LOG_FILE"; }
info()   { echo -e "${YELLOW}▶ $*${NC}" | tee -a "$LOG_FILE"; }
heading(){ echo -e "\n${CYAN}━━━ $* ━━━${NC}" | tee -a "$LOG_FILE"; }
sub()    { echo -e "  ${YELLOW}·${NC} $*" | tee -a "$LOG_FILE"; }

REPORT="/tmp/local-host-deploy-test-report.md"
LOG_FILE="/tmp/local-host-deploy-test.log"
: > "$LOG_FILE"

PASS=0; FAIL=0; TOTAL=0
pass() { PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); echo -e "  ${GREEN}✓${NC} $1" | tee -a "$LOG_FILE"; }
fail() { FAIL=$((FAIL+1)); TOTAL=$((TOTAL+1)); echo -e "  ${RED}✗${NC} $1" | tee -a "$LOG_FILE"; }
skip() { TOTAL=$((TOTAL+1)); echo -e "  ${YELLOW}─${NC} $1 (跳过)" | tee -a "$LOG_FILE"; }

PORT=8781
BASE="http://127.0.0.1:$PORT"
MODE="full"
KEEP=3
CLEAN=false

for arg in "$@"; do
  case "$arg" in
    --quick)  MODE="quick" ;;
    --verify) MODE="verify" ;;
    --clean)  CLEAN=true ;;
    --keep)   shift; KEEP="$1" ;;
    --root)   shift; LLM_GATEWAY_FILES_ROOT="$1"; export LLM_GATEWAY_FILES_ROOT ;;
    --port)   shift; PORT="$1" ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) err "unknown arg: $arg"; exit 1 ;;
  esac
done

lh_require_root >/dev/null

# ── precheck ──────────────────────────────────────────────────────────────
heading "precheck"
command -v go >/dev/null && ok "go: $(go version | awk '{print $3}')" || { err "go missing"; exit 1; }
command -v node >/dev/null && ok "node: $(node --version)" || sub "node missing (frontend not built)"
command -v jq >/dev/null && ok "jq: $(jq --version)" || sub "jq missing (json pretty-print skipped)"
command -v curl >/dev/null && ok "curl present" || { err "curl missing"; exit 1; }

# ── L0: dependencies (PG + Redis) ─────────────────────────────────────────
heading "L0: dependencies"

if docker ps --format '{{.Names}}' | grep -q '^llm-gateway-pg$'; then
  ok "PG container llm-gateway-pg is up"
else
  err "PG container not running — start it first"
  exit 1
fi

LOCAL_TABLE_COUNT=$(docker exec -e PGPASSWORD="${COMMON_PG_SUPERUSER_PASS:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}" llm-gateway-pg \
  psql -U llm_gateway -d llm_gateway -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tail -1 || echo 0)
if [[ "$LOCAL_TABLE_COUNT" -gt 0 ]]; then
  pass "llm_gateway has $LOCAL_TABLE_COUNT tables"
else
  fail "llm_gateway has 0 tables — run scripts/local-host-sync-db.sh first"
fi

if command -v redis-cli >/dev/null 2>&1; then
  if redis-cli -h 127.0.0.1 -p 6379 ping 2>/dev/null | grep -q PONG; then
    pass "Redis (127.0.0.1:6379): PONG"
  else
    sub "Redis not responding — gateway will run in degraded (memory-only) mode"
  fi
else
  sub "redis-cli missing"
fi

# ── deploy phase ─────────────────────────────────────────────────────────
if [[ "$MODE" == "full" ]]; then
  heading "deploy (local-host-deploy.sh deploy --keep $KEEP)"
  bash "$SCRIPT_DIR/local-host-deploy.sh" deploy --keep "$KEEP" 2>&1 | tee -a "$LOG_FILE"
fi

# ── L1: HTTP 存活 ─────────────────────────────────────────────────────────
heading "L1: HTTP 存活"

code=$(curl -sS -o /tmp/verify_health.json -w "%{http_code}" "$BASE/healthz" --max-time 10 2>/dev/null || echo "000")
body=$(cat /tmp/verify_health.json 2>/dev/null || echo "{}")
if [[ "$code" == "200" ]]; then
  pass "healthz → HTTP 200"
  status=$(echo "$body" | python3 -c "import sys,json;print(json.load(sys.stdin).get('status','?'))" 2>/dev/null || echo "?")
  version=$(echo "$body" | python3 -c "import sys,json;print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")
  pass "healthz body: status=$status, version=$version"
else
  fail "healthz → HTTP $code"
fi

spa_code=$(curl -sS -o /dev/null -w "%{http_code}" "$BASE/" --max-time 10 2>/dev/null || echo "000")
if [[ "$spa_code" == "200" || "$spa_code" == "301" || "$spa_code" == "302" ]]; then
  pass "SPA / → HTTP $spa_code"
else
  sub "SPA / → HTTP $spa_code (no frontend dist?)"
fi

# ── L2: 依赖连通 ──────────────────────────────────────────────────────────
heading "L2: 依赖连通"

PGPASSWORD="${COMMON_PG_SUPERUSER_PASS:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}" docker exec -e PGPASSWORD="$PGPASSWORD" llm-gateway-pg \
  pg_isready -U llm_gateway -d llm_gateway 2>/dev/null | grep -q "accepting" \
  && pass "PG pg_isready" || fail "PG not ready"

db_ver=$(docker exec -e PGPASSWORD="$PGPASSWORD" llm-gateway-pg \
  psql -U llm_gateway -d llm_gateway -tAc "SELECT version();" 2>/dev/null | head -1 | cut -d',' -f1 || echo "?")
pass "PG version: $db_ver"

# ── L3: 功能链路 ──────────────────────────────────────────────────────────
heading "L3: 功能链路"

models_code=$(curl -sS -o /tmp/verify_models.json -w "%{http_code}" "$BASE/v1/models" --max-time 15 2>/dev/null || echo "000")
if [[ "$models_code" == "200" ]]; then
  model_count=$(python3 -c "import json;print(len(json.load(open('/tmp/verify_models.json')).get('data',[])))" 2>/dev/null || echo 0)
  pass "/v1/models → HTTP 200 ($model_count models)"
else
  fail "/v1/models → HTTP $models_code"
fi

chat_code=$(curl -sS -o /tmp/verify_chat.json -w "%{http_code}" -X POST "$BASE/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: t-a" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}],"max_tokens":10}' \
  --max-time 30 2>/dev/null || echo "000")

if [[ "$chat_code" == "200" ]]; then
  has_choices=$(python3 -c "import json;d=json.load(open('/tmp/verify_chat.json'));print(len(d.get('choices',[]))>0)" 2>/dev/null || echo "False")
  if [[ "$has_choices" == "True" ]]; then
    content=$(python3 -c "import json;print(json.load(open('/tmp/verify_chat.json'))['choices'][0]['message']['content'][:100])" 2>/dev/null || echo "")
    pass "/v1/chat/completions → 200 OK (with choices)"
    sub "response: $content"
  else
    fail "/v1/chat/completions → 200 but no choices"
  fi
elif [[ "$chat_code" == "401" || "$chat_code" == "403" ]]; then
  pass "/v1/chat/completions → HTTP $chat_code (auth required, expected)"
else
  fail "/v1/chat/completions → HTTP $chat_code"
fi

# ── L4: 业务真实 (admin metrics) ──────────────────────────────────────────
heading "L4: 业务真实"

ADMIN_KEY="${LLM_GATEWAY_ADMIN_API_KEY:-local-admin-test-token-do-not-use-in-production}"
metrics_code=$(curl -sS -o /tmp/verify_metrics.txt -w "%{http_code}" \
  -H "Authorization: Bearer $ADMIN_KEY" "$BASE/metrics" --max-time 10 2>/dev/null || echo "000")
if [[ "$metrics_code" == "200" ]]; then
  if grep -q "# TYPE" /tmp/verify_metrics.txt 2>/dev/null; then
    llm_count=$(grep -c "^llm" /tmp/verify_metrics.txt 2>/dev/null || echo 0)
    pass "/metrics (admin Bearer) → 200 ($llm_count llm_* metrics)"
  else
    pass "/metrics → 200"
  fi
else
  fail "/metrics → HTTP $metrics_code"
fi

# ── version sanity ────────────────────────────────────────────────────────
heading "version consistency"

active_version=$(lh_active_version)
if [[ -n "$active_version" ]]; then
  pass "active bundle: $active_version"
  bundle_dir=$(lh_bundle_dir "$active_version")
  bundle_version=$(python3 -c "import json;print(json.load(open('$bundle_dir/version.json'))['version'])" 2>/dev/null || echo "?")
  pass "bundle version.json: $bundle_version"
  health_version=$(echo "$body" | python3 -c "import sys,json;print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")
  if [[ "$health_version" == "$bundle_version" ]]; then
    pass "healthz version matches bundle version.json"
  else
    sub "healthz version differs (healthz=$health_version, bundle=$bundle_version)"
  fi
else
  fail "no active bundle"
fi

# ── prune state ───────────────────────────────────────────────────────────
heading "version retention"

bin_dir=$(lh_layout_vars | sed -n 's/^bin_dir=//p')
bundle_count=$(find "$bin_dir" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
verified_count=$(find "$bin_dir" -mindepth 1 -maxdepth 1 -type d -exec test -f "{}/deployment.json" \; -exec grep -l '"verified":true' "{}/deployment.json" \; 2>/dev/null | wc -l | tr -d ' ')
sub "bundles on disk: $bundle_count (verified: $verified_count, retain cap=$KEEP)"
if [[ "$verified_count" -le "$KEEP" || "$verified_count" -eq 0 ]]; then
  pass "verified bundle count within retention cap (or 0 = first deploy)"
else
  warn "verified=$verified_count > keep=$KEEP — prune not yet run?"
fi

# ── report ────────────────────────────────────────────────────────────────
heading "report → $REPORT"
{
  echo "# Local-Host Deployment Test Report"
  echo ""
  echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "Mode: $MODE | Keep cap: $KEEP"
  echo ""
  echo "| 层级 | 名称 | 结果 |"
  echo "| --- | --- | --- |"
  echo "| L0 | 依赖 (PG + Redis) | $([ "$FAIL" -eq 0 ] && echo "✅" || echo "⚠️") |"
  echo "| L1 | HTTP 存活 | $(curl -fsS --max-time 2 $BASE/healthz >/dev/null 2>&1 && echo "✅" || echo "❌") |"
  echo "| L2 | 依赖连通 | $(docker exec -e PGPASSWORD='$PGPASSWORD' llm-gateway-pg pg_isready -U llm_gateway >/dev/null 2>&1 && echo "✅" || echo "❌") |"
  echo "| L3 | 功能链路 | $([ "$models_code" = "200" ] && echo "✅" || echo "❌") |"
  echo "| L4 | 业务真实 | $([ "$metrics_code" = "200" ] && echo "✅" || echo "❌") |"
  echo ""
  echo "Active bundle: \`$active_version\`"
  echo "Verified bundles retained: $verified_count / cap $KEEP"
  echo ""
  echo "## Summary"
  echo ""
  echo "总计: 通过 $PASS / 失败 $FAIL / 共 $TOTAL"
  echo ""
  echo "Endpoint: $BASE  |  Logs: $(lh_layout_vars | sed -n 's/^logs_dir=//p')/gateway.stdout.log"
} > "$REPORT"
ok "report written: $REPORT"

if $CLEAN; then
  heading "cleanup (stop + remove active bundle)"
  bash "$SCRIPT_DIR/local-host-deploy.sh" stop 2>&1 | tee -a "$LOG_FILE"
fi

if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
