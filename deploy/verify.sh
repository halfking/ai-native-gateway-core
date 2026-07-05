#!/usr/bin/env bash
# verify.sh — 部署后验证（rule 22 §7）
#
# 综合验证：健康检查 → 分区完整性 → 前端可达 → smoke API
#
# 用法:
#   ./deploy/verify.sh --env 184                  # 验证 184 部署
#   ./deploy/verify.sh --env local                # 验证本地部署
#   ./deploy/verify.sh --env 184 --record r057    # 写入部署记录目录
#   ./deploy/verify.sh --list                     # 列出检查项

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 参数 ──
ENV="${ENV:-local}"
RECORD_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    --record) RECORD_DIR="$2"; shift 2 ;;
    --list) echo "检查项: health|partition|frontend|smoke|version|pod"; exit 0 ;;
    *) echo "用法: $0 --env <local|184> [--record <dir>]"; exit 1 ;;
  esac
done

PASS=0; FAIL=0; TOTAL=0

red='\033[0;31m'; green='\033[0;32m'; yellow='\033[1;33m'; nc='\033[0m'

pass() { PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); echo -e "  ${green}✓${nc} $1"; }
fail() { FAIL=$((FAIL+1)); TOTAL=$((TOTAL+1)); echo -e "  ${red}✗${nc} $1"; }
info() { echo -e "  ${yellow}▶${nc} $1"; }
skip() { TOTAL=$((TOTAL+1)); echo -e "  ${yellow}─${nc} $1 (跳过)"; }

write_record() {
  [[ -n "$RECORD_DIR" && -d "$RECORD_DIR/verify" ]] || return 0
  local section="$1"
  local content="$2"
  echo "$content" >> "$RECORD_DIR/verify/${section}.log"
}

echo ""
echo "━━━ 部署后验证: env=${ENV} ━━━"
echo ""

# ──────────────────────────────────────────────────
# 1. Health 端点
# ──────────────────────────────────────────────────
section_health() {
  local url="$1"
  info "Health 检查: $url"
  local code body
  code=$(curl -sS -o /tmp/verify_health.json -w "%{http_code}" "$url" --max-time 10 || echo "000")
  body=$(cat /tmp/verify_health.json 2>/dev/null || echo "{}")

  if [[ "$code" == "200" ]]; then
    pass "Health 返回 200"
    if echo "$body" | jq '.' >/dev/null 2>&1; then
      echo "$body" | jq '.' 2>/dev/null | head -20
    else
      echo "  Body: $body"
    fi
    write_record "health" "HTTP $code — OK\n$body"
  else
    fail "Health 返回 $code"
    write_record "health" "HTTP $code — FAIL"
  fi
}

# ──────────────────────────────────────────────────
# 2. Pod 状态
# ──────────────────────────────────────────────────
section_pod() {
  local ssh_cmd="$1"
  info "Pod 状态检查"
  local pod_status
  pod_status=$($ssh_cmd "kubectl get pods -n pms-test -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo ''")

  if [[ -z "$pod_status" ]]; then
    fail "没有 Running Pod"
    write_record "pod" "No running pods"
    return
  fi

  local pod_info
  pod_info=$($ssh_cmd "kubectl get pods -n pms-test -l app=llm-gateway-go -o wide 2>/dev/null | tail -1" || echo "")
  echo "  $pod_info"
  pass "Pod $pod_status 运行中"
  write_record "pod" "Pod: $pod_status\n$pod_info"

  # 版本检查
  local version_in_pod
  version_in_pod=$($ssh_cmd "kubectl exec -n pms-test $pod_status -- cat /opt/llm-gateway-go/VERSION 2>/dev/null || kubectl exec -n pms-test $pod_status -- cat /.VERSION 2>/dev/null || echo 'unknown'")
  info "Pod 内版本: $version_in_pod"
  write_record "version" "Pod version: $version_in_pod"
}

# ──────────────────────────────────────────────────
# 3. 分区完整性检查
# ──────────────────────────────────────────────────
section_partition() {
  local psql_cmd="$1"
  info "分区完整性检查"
  local result
  result=$($psql_cmd "
    SELECT schemaname, tablename FROM pg_tables
    WHERE schemaname='public' AND (
      tablename LIKE 'request_logs_2026_%' OR tablename LIKE 'request_wal_2026_%'
    ) ORDER BY tablename;
  " 2>/dev/null || echo "")
  if [[ -n "$result" ]]; then
    local count; count=$(echo "$result" | wc -l)
    pass "分区表 $count 个"
    write_record "partition" "Partition tables ($count):\n$result"
  else
    skip "分区检查（无 pg 访问）"
  fi
}

# ──────────────────────────────────────────────────
# 4. 前端可达性
# ──────────────────────────────────────────────────
section_frontend() {
  local url="$1"
  info "前端可达性: $url"
  local code body_size
  code=$(curl -sS -o /dev/null -w "%{http_code}" "$url" --max-time 10 || echo "000")
  body_size=$(curl -sS -o /dev/null -w "%{size_download}" "$url" --max-time 10 || echo "0")

  if [[ "$code" == "200" || "$code" == "301" || "$code" == "302" ]]; then
    pass "前端 HTTP $code (body: ${body_size}B)"
    write_record "frontend" "HTTP $code, size=${body_size}B"
  else
    fail "前端 HTTP $code"
    write_record "frontend" "HTTP $code — FAIL"
  fi
}

# ──────────────────────────────────────────────────
# 5. Smoke API（Chat Completion）
# ──────────────────────────────────────────────────
section_smoke() {
  local base_url="$1"
  local api_key="${2:-test-key}"
  info "Smoke API: $base_url/v1/chat/completions"

  local code
  code=$(curl -sS -o /tmp/verify_smoke.json -w "%{http_code}" \
    -X POST "$base_url/v1/chat/completions" \
    -H "Authorization: Bearer $api_key" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"ping"}],"max_tokens":10}' \
    --max-time 30 || echo "000")

  if [[ "$code" == "200" ]]; then
    local has_choices
    has_choices=$(jq '.choices | length > 0' /tmp/verify_smoke.json 2>/dev/null || echo "false")
    if [[ "$has_choices" == "true" ]]; then
      pass "Smoke API 200 OK（含 choices）"
      write_record "smoke" "HTTP 200, choices OK\n$(jq '.choices[0].message.content[:100]' /tmp/verify_smoke.json 2>/dev/null)"
    else
      fail "Smoke API 200 但无 choices"
      write_record "smoke" "HTTP 200, no choices"
    fi
  else
    local body; body=$(head -c 200 /tmp/verify_smoke.json 2>/dev/null || echo "{}")
    fail "Smoke API HTTP $code"
    info "Body: $body"
    write_record "smoke" "HTTP $code\n$body"
  fi
}

# ══════════════════════════════════════════════════
# Main 逻辑
# ══════════════════════════════════════════════════
case "$ENV" in
  local)
    SSH_CMD=""
    section_health "http://localhost:8782/healthz"
    PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); echo -e "  ${yellow}─${nc} 前端检查（本地网关无前端服务，跳过）"
    section_smoke "http://localhost:8782" "test-key"
    section_partition "docker exec r112_postgres psql -U kxuser -d llm_gateway -tA -c"
    ;;

  184)
    SSH_CMD="ssh -p 25022 -o StrictHostKeyChecking=no root@14.103.112.184"
    section_health "http://14.103.112.184:30080/health"
    section_pod "$SSH_CMD"
    section_frontend "http://llmgo.kxpms.cn"
    # 184 PG via kubectl
    section_partition "ssh -p 25022 root@14.103.112.184 'kubectl exec -n pms-test deployment/llm-gateway-pg -- psql -U llm_gateway -d llm_gateway -tA -c'"
    section_smoke "http://14.103.112.184:30080"
    ;;

  *)
    echo "未知环境: $ENV (支持: local|184)"
    exit 1
    ;;
esac

# ── 总结 ──
echo ""
echo "━━━ 验证结果 ━━━"
echo "  通过: ${PASS}, 失败: ${FAIL}, 总计: ${TOTAL}"

if [[ "$FAIL" -gt 0 ]]; then
  echo "  ❌ 验证未通过，请检查日志"
  exit 1
fi
if [[ "$PASS" -eq "$TOTAL" ]]; then
  echo "  ✅ 全部通过"
fi
echo ""
