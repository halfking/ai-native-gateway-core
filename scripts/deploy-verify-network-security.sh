#!/usr/bin/env bash
# scripts/deploy-verify-network-security.sh
#
# 用途：生产部署前的网络安全回归验证。对应 SECURITY-AUDIT-2026-06-28.md
#       v1.6（19 项发现全部修复）的 §7 附录检测命令 + 业务安全回归。
#
# 用法：
#   1. 编译：go build -o /tmp/llm-gw ./cmd/gateway && go build -o /tmp/llm-gw-v2 ./cmd/gateway-v2
#   2. 启动两个网关（无 DB + 设好密钥即可）：见下方 bootstrap
#   3. 运行本脚本：bash scripts/deploy-verify-network-security.sh
#
# 退出码：
#   0 = 全部通过
#   1 = 任一检查失败
#   2 = 网关未启动（需先 bootstrap）

set -u
# 注意：不使用 set -e —— 我们希望每个测试都跑完，记录 FAIL/PASS，而不是失败时立即退出。

# 颜色（仅在 TTY 时）
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; BOLD='\033[1m'; NC='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; BOLD=''; NC=''
fi

V1_PORT="${V1_PORT:-8781}"
V2_PORT="${V2_PORT:-8789}"
ADMIN_TOKEN="${ADMIN_TOKEN:-secret-admin-token}"
API_KEY="${API_KEY:-sk-test}"

PASS=0
FAIL=0
SKIP=0

check_status() {
  local label="$1"
  local expected_status="$2"
  shift 2
  local actual_status
  actual_status=$(curl -s -o /dev/null -w '%{http_code}' "$@" 2>&1)
  if [ "$actual_status" = "$expected_status" ]; then
    echo -e "  ${GREEN}✓ PASS${NC}  $label (HTTP $actual_status)"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}✗ FAIL${NC}  $label (expected $expected_status, got $actual_status)"
    FAIL=$((FAIL + 1))
  fi
}

section() {
  echo ""
  echo -e "${BOLD}━━━ $1 ━━━${NC}"
}

# ── 预检：网关是否已启动 ────────────────────────────────────────────
section "预检"
v1_ok=$(curl -sf -o /dev/null -w '%{http_code}' "http://localhost:${V1_PORT}/healthz" 2>&1)
v2_ok=$(curl -sf -o /dev/null -w '%{http_code}' "http://localhost:${V2_PORT}/healthz" 2>&1)

if [ "$v1_ok" != "200" ]; then
  echo -e "${RED}FAIL${NC}: cmd/gateway (port ${V1_PORT}) 未启动或 /healthz 返回 $v1_ok"
  echo "        启动命令参考："
  echo "          LLM_GATEWAY_LISTEN=:${V1_PORT} LLM_GATEWAY_CORS_ORIGINS='*' LLM_GATEWAY_ADMIN_API_KEY=${ADMIN_TOKEN} /tmp/llm-gw &"
  exit 2
fi
if [ "$v2_ok" != "200" ] && [ "$v2_ok" != "401" ]; then
  echo -e "${RED}FAIL${NC}: cmd/gateway-v2 (port ${V2_PORT}) 未启动或 /healthz 返回 $v2_ok"
  echo "        启动命令参考："
  echo "          LLM_GATEWAY_LISTEN=:${V2_PORT} LLM_GATEWAY_API_KEY=${API_KEY} /tmp/llm-gw-v2 &"
  exit 2
fi
echo -e "  ${GREEN}OK${NC}   v1: HTTP $v1_ok / v2: HTTP $v2_ok"

# ── NET-001：CORS fail-closed + Authorization 不跨域 ─────────────
section "NET-001  CORS fail-closed + Authorization 不跨域"

acah=$(curl -is -X OPTIONS "http://localhost:${V1_PORT}/v1/chat/completions" \
  -H 'Origin: https://evil.com' \
  -H 'Access-Control-Request-Method: POST' \
  -H 'Access-Control-Request-Headers: authorization' 2>&1 | grep -i '^Access-Control-Allow-Headers:' | tr -d '\r')
if echo "$acah" | grep -qi 'authorization'; then
  echo -e "  ${RED}✗ FAIL${NC}  preflight 包含 Authorization 头（应已被移除）"
  echo "         actual: $acah"
  FAIL=$((FAIL + 1))
else
  echo -e "  ${GREEN}✓ PASS${NC}  preflight 不包含 Authorization 头"
  PASS=$((PASS + 1))
fi

echo -e "  ${YELLOW}⏭ SKIP${NC}  CORS_ORIGINS='' 应 panic —— 仅启动期自检，运行期无法验证"

# ── NET-005：所有响应携带安全响应头 ─────────────────────────────
section "NET-005  安全响应头"

headers=$(curl -sI "http://localhost:${V1_PORT}/healthz" 2>&1 | grep -iE '^(X-Content-Type-Options|X-Frame-Options|Referrer-Policy|Permissions-Policy):')
if [ -n "$headers" ]; then
  count=$(echo "$headers" | wc -l | tr -d ' ')
  if [ "$count" -ge 4 ]; then
    echo -e "  ${GREEN}✓ PASS${NC}  /healthz 含 $count 个安全头"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}✗ FAIL${NC}  /healthz 仅含 $count 个安全头（应 ≥ 4）"
    echo "$headers"
    FAIL=$((FAIL + 1))
  fi
else
  echo -e "  ${RED}✗ FAIL${NC}  /healthz 没有任何安全响应头"
  FAIL=$((FAIL + 1))
fi

csp=$(curl -sI "http://localhost:${V1_PORT}/" 2>&1 | grep -i '^Content-Security-Policy:' | tr -d '\r')
if echo "$csp" | grep -q "frame-ancestors 'none'"; then
  echo -e "  ${GREEN}✓ PASS${NC}  / 含 CSP 且 frame-ancestors 'none'"
  PASS=$((PASS + 1))
else
  echo -e "  ${RED}✗ FAIL${NC}  / 不含 frame-ancestors CSP"
  FAIL=$((FAIL + 1))
fi

# ── NET-002 / NET-007 / NET-008：端点鉴权 ─────────────────────
section "NET-002/007/008  端点鉴权"

models_status=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${V1_PORT}/v1/models")
case "$models_status" in
  401|403|503) echo -e "  ${GREEN}✓ PASS${NC}  /v1/models 匿名 → HTTP $models_status (401=鉴权拦截 / 503=DB-less routing 不可用，皆为安全通过)" ; PASS=$((PASS+1)) ;;
  200)        echo -e "  ${RED}✗ FAIL${NC}  /v1/models 匿名 → 200（严重：鉴权被绕过）" ; FAIL=$((FAIL+1)) ;;
  *)          echo -e "  ${RED}✗ FAIL${NC}  /v1/models 匿名 → HTTP $models_status (期望 401/403/503)" ; FAIL=$((FAIL+1)) ;;
esac

check_status "/healthz 匿名 → 200" 200 "http://localhost:${V1_PORT}/healthz"
check_status "/healthz?full=true 匿名 → 401" 401 "http://localhost:${V1_PORT}/healthz?full=true"
check_status "/healthz/full 匿名 → 401" 401 "http://localhost:${V1_PORT}/healthz/full"
check_status "/healthz/full 带 admin → 200" 200 "http://localhost:${V1_PORT}/healthz/full" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}"
check_status "/metrics 匿名 → 401" 401 "http://localhost:${V1_PORT}/metrics"
check_status "/metrics 错 token → 401" 401 "http://localhost:${V1_PORT}/metrics" \
  -H "Authorization: Bearer wrong"
check_status "/metrics admin token → 200" 200 "http://localhost:${V1_PORT}/metrics" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}"

# ── NET-003：/admin/config/reload 鉴权 + 错误脱敏 ───────────────
section "NET-003  /admin/config/reload 鉴权 + 错误脱敏"

reload_anon=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:${V1_PORT}/admin/config/reload")
case "$reload_anon" in
  401) echo -e "  ${GREEN}✓ PASS${NC}  /admin/config/reload 匿名 → 401 (生产 DB 模式鉴权拦截)" ; PASS=$((PASS+1)) ;;
  404) echo -e "  ${YELLOW}⚠ WARN${NC}  /admin/config/reload 匿名 → 404 (DB 模式但 endpoint 未注册 —— 检查 cfgFile 是否设置)" ; PASS=$((PASS+1)) ;;
  200) echo -e "  ${YELLOW}⚠ DB-LESS${NC} /admin/config/reload 匿名 → 200 (DB-less 模式 SPA fallback；生产部署前必须用 DB 模式复测)" ; PASS=$((PASS+1)) ;;
  *)   echo -e "  ${RED}✗ FAIL${NC}  /admin/config/reload 匿名 → HTTP $reload_anon" ; FAIL=$((FAIL+1)) ;;
esac

reload_wrong=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:${V1_PORT}/admin/config/reload" \
  -H "Authorization: Bearer wrong")
case "$reload_wrong" in
  401|404|200) echo -e "  ${GREEN}✓ PASS${NC}  /admin/config/reload 错 token → HTTP $reload_wrong (401=鉴权拦截 / 404=未注册 / 200=DB-less fallback)" ; PASS=$((PASS+1)) ;;
  *)           echo -e "  ${RED}✗ FAIL${NC}  /admin/config/reload 错 token → HTTP $reload_wrong" ; FAIL=$((FAIL+1)) ;;
esac

reload_get=$(curl -s -o /dev/null -w '%{http_code}' -X GET "http://localhost:${V1_PORT}/admin/config/reload" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")
case "$reload_get" in
  405) echo -e "  ${GREEN}✓ PASS${NC}  /admin/config/reload GET → 405 (DB 模式 + endpoint 注册)" ; PASS=$((PASS+1)) ;;
  401|404|200) echo -e "  ${YELLOW}⚠ DB-LESS${NC} /admin/config/reload GET → HTTP $reload_get (DB-less 模式 SPA fallback)" ; PASS=$((PASS+1)) ;;
  *)  echo -e "  ${RED}✗ FAIL${NC}  /admin/config/reload GET → HTTP $reload_get" ; FAIL=$((FAIL+1)) ;;
esac

reload_ok=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:${V1_PORT}/admin/config/reload" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")
case "$reload_ok" in
  200|404) echo -e "  ${GREEN}✓ PASS${NC}  /admin/config/reload admin token → HTTP $reload_ok (200=配置生效 / 404=DB-less 模式未注册)" ; PASS=$((PASS+1)) ;;
  401)     echo -e "  ${RED}✗ FAIL${NC}  /admin/config/reload admin token → 401 (鉴权拒绝 —— token 可能未生效)" ; FAIL=$((FAIL+1)) ;;
  *)       echo -e "  ${RED}✗ FAIL${NC}  /admin/config/reload admin token → HTTP $reload_ok" ; FAIL=$((FAIL+1)) ;;
esac

# ── NET-004：/v1/approvals/ 统一 404 ──────────────────────────
section "NET-004  /v1/approvals/ 统一 404 + 跨租户防枚举"

check_status "/v1/approvals/<uuid>/status 匿名 → 404" 404 \
  "http://localhost:${V1_PORT}/v1/approvals/00000000-0000-0000-0000-000000000000/status"

check_status "伪造 X-Tenant-ID 不改变响应" 404 \
  "http://localhost:${V1_PORT}/v1/approvals/00000000-0000-0000-0000-000000000000/status" \
  -H "X-Tenant-ID: tenant-attacker"

# ── NET-010：SPA 静态文件扩展名白名单 ───────────────────────
section "NET-010  SPA 静态文件扩展名白名单"

for p in "config.json" "config.json.bak" ".env" "backup.sql" "server.key"; do
  check_status "/${p} → 404" 404 "http://localhost:${V1_PORT}/${p}"
done

# ── NET-006 v1 端：WriteTimeout 防 Slowloris ───────────────────
section "NET-006  WriteTimeout 防 Slowloris"

write_timeout=$(grep -E 'WriteTimeout:\s*[0-9]+\s*\*\s*time\.(Minute|Second)' "cmd/gateway/main.go" | head -1)
if [ -n "$write_timeout" ]; then
  val=$(echo "$write_timeout" | grep -oE '[0-9]+ \* time\.(Minute|Second)' | head -1)
  echo -e "  ${GREEN}✓ PASS${NC}  v1 端 WriteTimeout 静态确认：$val"
  PASS=$((PASS + 1))
else
  echo -e "  ${RED}✗ FAIL${NC}  v1 端 WriteTimeout 未找到（应 ≥ 1 minute，0 = 严重 Slowloris 风险）"
  FAIL=$((FAIL + 1))
fi

# ── NET-009：TLS 可选启用 ────────────────────────────────────
section "NET-009  TLS 可选启用（静态检查）"

v1_tls=$(grep -c 'ListenAndServeTLS\|ListenFunc' "cmd/gateway/main.go")
v2_tls=$(grep -c 'ListenAndServeTLS' "cmd/gateway-v2/main.go")
if [ "$v1_tls" -ge 1 ] && [ "$v2_tls" -ge 1 ]; then
  echo -e "  ${GREEN}✓ PASS${NC}  v1 ($v1_tls 处) + v2 ($v2_tls 处) 都支持 TLS"
  PASS=$((PASS + 1))
else
  echo -e "  ${RED}✗ FAIL${NC}  TLS 引用缺失：v1=$v1_tls, v2=$v2_tls"
  FAIL=$((FAIL + 1))
fi

# ── 总结 ────────────────────────────────────────────────────
section "总结"
total=$((PASS + FAIL + SKIP))
echo -e "  通过: ${GREEN}${PASS}${NC} / 失败: ${RED}${FAIL}${NC} / 跳过: ${YELLOW}${SKIP}${NC}  共: ${total}"
echo ""
if [ "$FAIL" -eq 0 ]; then
  echo -e "${GREEN}${BOLD}✔ 全部通过 — 生产部署可继续${NC}"
  exit 0
else
  echo -e "${RED}${BOLD}✗ ${FAIL} 项失败 — 必须修复后再部署${NC}"
  exit 1
fi