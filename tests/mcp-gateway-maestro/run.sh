#!/usr/bin/env bash
# tests/mcp-gateway-maestro/run.sh —— MCP 网关的 Maestro 方式全面验证
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ 三层验证（对应仓内「Maestro」惯例：观测 → 假设 → 最小修复 → 再测）：    │
# │   L1 Go 测试    —— go test ./mcp/... （治理链单测/集成测）               │
# │   L2 API 场景   —— 本脚本的 S01~S31（真实进程 + 真实 HTTP）              │
# │   L3 浏览器 flow —— maestro/flows/10-mcp-gateway.yaml（运营看板逐页断言） │
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   bash tests/mcp-gateway-maestro/run.sh                # 三层全跑
#   SKIP_GO=1 bash tests/mcp-gateway-maestro/run.sh      # 跳过 L1
#   SKIP_MAESTRO=1 bash tests/mcp-gateway-maestro/run.sh  # 跳过 L3
#
# 产物：tests/mcp-gateway-maestro/reports/<时间戳>/{summary.md,go-test.log,maestro.log}
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

REPORT_DIR="${ROOT}/tests/mcp-gateway-maestro/reports"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="${REPORT_DIR}/${STAMP}"
mkdir -p "${OUT}"

ADMIN_TOKEN="maestro-admin-token"
pass=0; fail=0
declare -a FAILED_CASES=()
declare -a ALL_CASES=()   # 全部用例读数（写进 summary.md ⇒ 报告产物自带证据）

log()  { printf '%s\n' "$*"; }
ok()   { pass=$((pass+1)); ALL_CASES+=("✅ $*"); printf '  ✅ %s\n' "$*"; }
bad()  { fail=$((fail+1)); FAILED_CASES+=("$*"); ALL_CASES+=("❌ $*"); printf '  ❌ %s\n' "$*"; }

# ── 0. 工具链探测 ───────────────────────────────────────────────────────────
# 构建与测试用两套命令：
#   · 构建要产出**宿主可执行**的二进制 ⇒ 容器内必须交叉编译（GOOS/GOARCH=宿主）。
#   · 测试在容器内**原生**跑 ⇒ 绝不能带 GOOS/GOARCH（否则测试二进制无法执行）。
BUILD_CMD=(); TEST_CMD=()
if command -v go >/dev/null 2>&1; then
  # ★ 专用构建缓存（r1048 门：在作用域内的编译型脚本必须指向仓内缓存，
  #   不写用户全局 GOCACHE —— 2026-11-09 台账 165→166 收口）。
  if [ -z "${GOCACHE:-}" ]; then
    __gw_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
    [ -n "$__gw_root" ] && export GOCACHE="$("${__gw_root}/scripts/lib/gocache.sh" test 2>/dev/null || true)"
  fi
  BUILD_CMD=(go); TEST_CMD=(go)
elif command -v docker >/dev/null 2>&1; then
  HOST_OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  HOST_ARCH="$(uname -m)"
  case "${HOST_ARCH}" in
    arm64|aarch64) HOST_ARCH=arm64 ;;
    x86_64|amd64)  HOST_ARCH=amd64 ;;
  esac
  BUILD_CMD=(docker run --rm -v "${ROOT}:/src" -w /src -e GOFLAGS=-mod=vendor -e CGO_ENABLED=0
             -e "GOOS=${HOST_OS}" -e "GOARCH=${HOST_ARCH}" golang:1.27.2-alpine go)
  TEST_CMD=(docker run --rm -v "${ROOT}:/src" -w /src -e GOFLAGS=-mod=vendor -e CGO_ENABLED=0
            golang:1.27.2-alpine go)
else
  log "!! 既没有 go 也没有 docker，无法构建 MCP 网关二进制"; exit 2
fi

# 选空闲端口：机器上常驻着别的本地服务（如 :8799 被占用）⇒ 固定端口会静默串台。
pick_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

PORT="${MCP_TEST_PORT:-$(pick_port)}"
LIMIT_PORT="${MCP_TEST_LIMIT_PORT:-$(pick_port)}"
BOOT_PORT="${MCP_TEST_BOOT_PORT:-$(pick_port)}"
BODY_PORT="${MCP_TEST_BODY_PORT:-$(pick_port)}"
BASE_URL="http://127.0.0.1:${PORT}"
BIN="${ROOT}/.build-local/mcp-gateway-maestro-${STAMP}"
mkdir -p "${ROOT}/.build-local"

# ── 1. 构建与启动 ───────────────────────────────────────────────────────────
log "== 构建 cmd/mcp-gateway"
if [ ${#BUILD_CMD[@]} -gt 1 ]; then
  "${BUILD_CMD[@]}" build -o "/src/.build-local/mcp-gateway-maestro-${STAMP}" ./cmd/mcp-gateway \
    || { log "!! 构建失败"; exit 2; }
else
  "${BUILD_CMD[@]}" build -o "${BIN}" ./cmd/mcp-gateway || { log "!! 构建失败"; exit 2; }
fi

GATEWAY_PID=""; LIMIT_PID=""; BOOT_PID=""; BODY_PID=""
cleanup() {
  [ -n "${GATEWAY_PID}" ] && kill "${GATEWAY_PID}" 2>/dev/null
  [ -n "${LIMIT_PID}" ]  && kill "${LIMIT_PID}" 2>/dev/null
  [ -n "${BOOT_PID}" ]   && kill "${BOOT_PID}" 2>/dev/null
  [ -n "${BODY_PID}" ]   && kill "${BODY_PID}" 2>/dev/null
  rm -f "${BIN}"
  return 0
}
trap cleanup EXIT

start_gateway() { # start_gateway <port> <read_limit> <bootstrap_enabled> <logfile> [max_body_bytes]
  MCP_GATEWAY_ADMIN_TOKEN="${ADMIN_TOKEN}" \
  MCP_GATEWAY_READ_RATE_LIMIT="$2" \
  MCP_GATEWAY_BOOTSTRAP_ENABLED="$3" \
  MCP_GATEWAY_BOOTSTRAP_AUTO_APPROVE=true \
  MCP_GATEWAY_MAX_BODY_BYTES="${5:-1048576}" \
    "${BIN}" --listen "127.0.0.1:$1" --demo --admin-token "${ADMIN_TOKEN}" \
    >"$4" 2>&1 &
  echo $!
}

# 探针端点：/healthz 是公开自检（/health 需要管理令牌，探针不可用）。
wait_ready() { # wait_ready <base-url>
  for _ in $(seq 1 40); do
    curl -s --max-time 2 "$1/mcp/control/healthz" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

log "== 启动 MCP 网关（${BASE_URL}）"
GATEWAY_PID="$(start_gateway "${PORT}" 120 false "${OUT}/gateway-${PORT}.log")"
if ! wait_ready "${BASE_URL}"; then
  log "!! 网关未就绪（看 ${OUT}/gateway-${PORT}.log）"; exit 2
fi
DEMO_KEY="$(grep -m1 '^DEMO_API_KEY=' "${OUT}/gateway-${PORT}.log" | cut -d= -f2-)"
if [ -z "${DEMO_KEY}" ]; then
  log "!! 未取到演示凭证（看 ${OUT}/gateway-${PORT}.log）"; exit 2
fi
log "   演示凭证：${DEMO_KEY:0:14}…"

# ── 2. L1：Go 测试 ───────────────────────────────────────────────────────────
if [ "${SKIP_GO:-0}" != "1" ]; then
  log "== L1: go test ./mcp/..."
  "${TEST_CMD[@]}" test ./mcp/... >"${OUT}/go-test.log" 2>&1
  if [ $? -eq 0 ]; then ok "L1 go test ./mcp/... 全绿"; else bad "L1 go test ./mcp/... 失败（见 go-test.log）"; fi
fi

# ── 3. L2：API 级场景 ───────────────────────────────────────────────────────
rpc() { # rpc <base-url> <path> <key> <json-body> [extra-header]
  local base="$1" path="$2" key="$3" body="$4" extra="${5:-}"
  local args=(-s -o "${OUT}/.body" -w '%{http_code}' -X POST "${base}${path}"
              -H 'Content-Type: application/json')
  [ -n "${key}" ] && args+=(-H "Authorization: Bearer ${key}")
  [ -n "${extra}" ] && args+=(-H "${extra}")
  args+=(-d "${body}")
  curl "${args[@]}"
}
# rpc_h 同 rpc，但**额外把响应头**落到 ${OUT}/.hdr（验 Retry-After 等头用）。
rpc_h() { # rpc_h <base-url> <path> <key> <json-body> [extra-header]
  local base="$1" path="$2" key="$3" body="$4" extra="${5:-}"
  local args=(-s -D "${OUT}/.hdr" -o "${OUT}/.body" -w '%{http_code}' -X POST "${base}${path}"
              -H 'Content-Type: application/json')
  [ -n "${key}" ] && args+=(-H "Authorization: Bearer ${key}")
  [ -n "${extra}" ] && args+=(-H "${extra}")
  args+=(-d "${body}")
  curl "${args[@]}"
}
hdr_get() { # hdr_get <头名> —— 从 .hdr 取响应头值
  tr -d '\r' < "${OUT}/.hdr" | awk -v h="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" '
    BEGIN{FS=": "} {k=tolower($1); if(k==h){sub(/^[^:]*: /,""); print; exit}}'
}
body_get() { cat "${OUT}/.body"; }
jq_get() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

log "== L2: API 场景（S01~S31）"

# S01 无凭证 → 401
code="$(rpc "${BASE_URL}" /mcp '' '{"jsonrpc":"2.0","id":1,"method":"initialize"}')"
[ "${code}" = "401" ] && ok "S01 无凭证访问数据面 → 401" || bad "S01 无凭证 → ${code}（期望 401）"

# S02 initialize → 协议版本
code="$(rpc "${BASE_URL}" /mcp "${DEMO_KEY}" '{"jsonrpc":"2.0","id":1,"method":"initialize"}')"
ver="$(body_get | jq_get 'd.get("result",{}).get("protocolVersion","")')"
[ "${code}" = "200" ] && [ -n "${ver}" ] && ok "S02 initialize → 协议版本 ${ver}" || bad "S02 initialize → code=${code} ver=${ver}"

# S03 tools/list → 命名空间化
code="$(rpc "${BASE_URL}" /mcp "${DEMO_KEY}" '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')"
tools="$(body_get | jq_get '" ".join(t["name"] for t in d["result"]["tools"])')"
case "${tools}" in
  *demo__search_memory*) ok "S03 tools/list 聚合上游工具 → ${tools}" ;;
  *) bad "S03 tools/list 未见 demo__search_memory（${tools}）" ;;
esac

# S04 tools/call → 成功
code="$(rpc "${BASE_URL}" /mcp "${DEMO_KEY}" '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo__search_memory","arguments":{"q":"hello"}}}')"
case "$(body_get)" in
  *demo\ upstream\ handled*) ok "S04 tools/call 成功转发到上游" ;;
  *) bad "S04 tools/call 结果异常 code=${code}: $(body_get)" ;;
esac

# S05 控制面无令牌 → 401
code="$(curl -s -o "${OUT}/.body" -w '%{http_code}' "${BASE_URL}/mcp/control/tools")"
[ "${code}" = "401" ] && ok "S05 控制面无管理令牌 → 401" || bad "S05 控制面无令牌 → ${code}（期望 401）"

# S06 控制面注册适配器 → 201
code="$(curl -s -o "${OUT}/.body" -w '%{http_code}' -X POST "${BASE_URL}/mcp/control/adapters" \
  -H "X-Admin-Token: ${ADMIN_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"name":"crm","endpoint":"http://127.0.0.1:8420","capability":"code","enabled":true,"sensitivity":"write"}')"
[ "${code}" = "201" ] && ok "S06 注册适配器 → 201" || bad "S06 注册适配器 → ${code}（期望 201）"

# S07 注册高敏工具 → 201
code="$(curl -s -o "${OUT}/.body" -w '%{http_code}' -X POST "${BASE_URL}/mcp/control/tools" \
  -H "X-Admin-Token: ${ADMIN_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"name":"crm__delete_customer","adapter":"crm","sensitivity":"high","require_approval":true,"visibility":"public","group":"crm-admin","enabled":true}')"
[ "${code}" = "201" ] && ok "S07 注册已启用高敏工具（需审批）→ 201" || bad "S07 注册工具 → ${code}（期望 201）"

# S08 签发受限凭证 → 201，明文一次性
code="$(curl -s -o "${OUT}/.body" -w '%{http_code}' -X POST "${BASE_URL}/mcp/control/keys" \
  -H "X-Admin-Token: ${ADMIN_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"tenant_id":"default","name":"maestro-agent","scopes":["mcp.read","mcp.execute"],"allowed_adapters":["demo"]}')"
SECRET="$(body_get | jq_get 'd.get("secret","")')"
[ "${code}" = "201" ] && [ -n "${SECRET}" ] && ok "S08 签发凭证 → 201（明文 sk-mcp-*）" || bad "S08 签发凭证 → code=${code} secret=${SECRET}"

# S09 受限凭证可调用 demo 工具
code="$(rpc "${BASE_URL}" /mcp "${SECRET}" '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"demo__search_memory","arguments":{"q":"x"}}}')"
[ "${code}" = "200" ] && ok "S09 受限凭证调用允许的工具 → 200" || bad "S09 受限凭证调用 → ${code}（期望 200）"

# S10 越权工具（适配器白名单外）→ 403/404
#   ★ S08 的凭证只允许 demo 适配器 ⇒ 调 crm 工具被「适配器白名单」闸挡下。
code="$(rpc "${BASE_URL}" /mcp "${SECRET}" '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"crm__delete_customer","arguments":{}}}')"
code32003="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
case "${code}" in
  403|404) ok "S10 越权访问被拒 → ${code}（JSON-RPC 码 ${code32003}）" ;;
  *) bad "S10 越权访问 → ${code}（期望 403/404）" ;;
esac
# 既有 tool-allow 的拒绝码必须逐字沿用（-32003）
[ "${code32003}" = "-32003" ] && ok "S10b 工具拒绝码 → -32003（与既有 tool-allow 一致）" \
  || bad "S10b 工具拒绝码 → ${code32003}（期望 -32003）"

# S11 高敏工具缺审批凭据 → 403
#   ★ 用一把**允许访问 crm 适配器**的凭证 ⇒ 这样被拒的才是「审批闸」，
#     而不是被上一道「适配器白名单」闸提前挡下（两者都返回 403，
#     但测的是不同的闸，分开才能定位问题）。
APPROVAL_KEY="$(curl -s -X POST "${BASE_URL}/mcp/control/keys" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"default","user_id":"demo-user","name":"maestro-approver","scopes":["mcp.read","mcp.execute"],"allowed_adapters":["demo","crm"]}' \
  | jq_get 'd.get("secret","")')"
code="$(rpc "${BASE_URL}" /mcp "${APPROVAL_KEY:-${DEMO_KEY}}" '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"crm__delete_customer","arguments":{}}}')"
apr_code="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
if [ "${code}" = "403" ] && [ "${apr_code}" = "-32007" ]; then
  ok "S11 高敏工具缺审批凭据 → 403（JSON-RPC 码 -32007，审批专属码）"
else
  bad "S11 高敏工具 → ${code} 码=${apr_code}（期望 403 / -32007）"
fi

# S12 审批通过后放行
APR="$(curl -s -X POST "${BASE_URL}/mcp/control/approvals" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' -d '{"tool":"crm__delete_customer","requester":"demo-user"}' \
  | jq_get 'd.get("id","")')"
curl -s -o /dev/null -X POST "${BASE_URL}/mcp/control/approvals/${APR}" \
  -H "X-Admin-Token: ${ADMIN_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"decision":"approved","decided_by":"maestro"}'
code="$(rpc "${BASE_URL}" /mcp "${APPROVAL_KEY:-${DEMO_KEY}}" '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"crm__delete_customer","arguments":{}}}' "X-MCP-Approval: ${APR}")"
case "${code}" in
  200|502|503) ok "S12 带审批凭据后放行（${code}：上游未就绪也算治理链通过）" ;;
  403) bad "S12 带审批凭据仍被拒 → ${code}" ;;
  *) bad "S12 带审批凭据 → ${code}" ;;
esac

# S13 吊销凭证 → 立即 401
KEYID="$(curl -s "${BASE_URL}/mcp/control/keys" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  | jq_get 'next((k["id"] for k in d["keys"] if k.get("name")=="maestro-agent"),"")')"
curl -s -o /dev/null -X POST "${BASE_URL}/mcp/control/keys/${KEYID}/revoke" -H "X-Admin-Token: ${ADMIN_TOKEN}"
code="$(rpc "${BASE_URL}" /mcp "${SECRET}" '{"jsonrpc":"2.0","id":8,"method":"tools/list"}')"
rev_code="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
if [ "${code}" = "401" ] && [ "${rev_code}" = "-32001" ]; then
  ok "S13 吊销后立即 401（JSON-RPC 码 -32001，与既有 bindings 一致）"
else
  bad "S13 吊销后 → ${code} 码=${rev_code}（期望 401 / -32001）"
fi

# S14 审计与导出
AUDIT="$(curl -s "${BASE_URL}/mcp/control/audit?limit=200" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  | jq_get 'len(d.get("events",[]))')"
[ "${AUDIT}" -gt 0 ] 2>/dev/null && ok "S14 审计事件 ${AUDIT} 条" || bad "S14 审计为空（${AUDIT}）"

EXPORT_OK="$(curl -s "${BASE_URL}/mcp/control/audit/export" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  | python3 -c '
import json,sys
n=0
for line in sys.stdin:
    line=line.strip()
    if line:
        json.loads(line); n+=1
print(n)' 2>/dev/null)"
[ "${EXPORT_OK}" -gt 0 ] 2>/dev/null && ok "S14b 审计导出 JSONL 可解析（${EXPORT_OK} 行）" || bad "S14b 审计导出异常（${EXPORT_OK}）"

# S15 RFC 9728 受保护资源元数据
RES="$(curl -s "${BASE_URL}/.well-known/oauth-protected-resource" | jq_get 'd.get("resource","")')"
[ -n "${RES}" ] && ok "S15 RFC 9728 受保护资源元数据 → ${RES}" || bad "S15 元数据缺失"

# S16 限流 → 429（独立实例，读类限额 1/分钟）
log "== S16 限流场景（独立实例，读类限额=1/分钟）"
LIMIT_PID="$(start_gateway "${LIMIT_PORT}" 1 false "${OUT}/gateway-limit.log")"
if wait_ready "http://127.0.0.1:${LIMIT_PORT}"; then
  LIMIT_KEY="$(grep -m1 '^DEMO_API_KEY=' "${OUT}/gateway-limit.log" | cut -d= -f2-)"
  c1="$(rpc "http://127.0.0.1:${LIMIT_PORT}" /mcp "${LIMIT_KEY}" '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo__search_memory","arguments":{"q":"a"}}}')"
  c2="$(rpc_h "http://127.0.0.1:${LIMIT_PORT}" /mcp "${LIMIT_KEY}" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"demo__search_memory","arguments":{"q":"b"}}}')"
  if [ "${c1}" = "200" ] && [ "${c2}" = "429" ]; then
    ok "S16 限流：第 1 次 200，第 2 次 429"
  else
    bad "S16 限流：c1=${c1} c2=${c2}（期望 200/429）"
  fi
  # ★ 既有 429 契约是「三件套同源」：Retry-After 头(秒,ceil) + -32002 +
  #   data.retry_after_ms(毫秒) + data.limit ⇒ 逐项断言，避免只看状态码就判绿。
  retry_hdr="$(hdr_get Retry-After)"
  rl_code="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
  rl_ms="$(body_get | jq_get 'd.get("error",{}).get("data",{}).get("retry_after_ms",0)')"
  rl_limit="$(body_get | jq_get 'd.get("error",{}).get("data",{}).get("limit",0)')"
  if [ -n "${retry_hdr}" ] && [ "${rl_code}" = "-32002" ] && [ "${rl_ms}" != "0" ] && [ "${rl_limit}" = "1" ]; then
    ok "S16b 429 三件套：Retry-After=${retry_hdr}s，码 ${rl_code}，retry_after_ms=${rl_ms}，limit=${rl_limit}"
  else
    bad "S16b 429 三件套缺失：Retry-After='${retry_hdr}' 码=${rl_code} retry_after_ms=${rl_ms} limit=${rl_limit}"
  fi
else
  bad "S16 限流实例未就绪"
fi

# S17~S19 密钥引导面（免认证 D2/D3）
log "== S17~S19 密钥引导面（apply/status/claim）"
BOOT_PID="$(start_gateway "${BOOT_PORT}" 120 true "${OUT}/gateway-boot.log")"
if wait_ready "http://127.0.0.1:${BOOT_PORT}"; then
  BOOT="http://127.0.0.1:${BOOT_PORT}"
  APPLY="$(curl -s -X POST "${BOOT}/mcp/control/keys/apply" -H 'Content-Type: application/json' \
    -d '{"idempotencyKey":"maestro-idem-1","deviceName":"maestro-device","name":"bootstrap-agent"}')"
  REQ_ID="$(printf '%s' "${APPLY}" | jq_get 'd.get("requestId","")')"
  CLAIM_TOKEN="$(printf '%s' "${APPLY}" | jq_get 'd.get("claimToken","")')"
  STATUS="$(printf '%s' "${APPLY}" | jq_get 'd.get("status","")')"
  if [ -n "${REQ_ID}" ] && [ "${STATUS}" = "approved" ]; then
    ok "S17 引导申请 → approved（requestId=${REQ_ID}）"
  else
    bad "S17 引导申请异常：${APPLY}"
  fi

  # 幂等重放：同一 idempotencyKey 返回同一 requestId
  APPLY2="$(curl -s -X POST "${BOOT}/mcp/control/keys/apply" -H 'Content-Type: application/json' \
    -d '{"idempotencyKey":"maestro-idem-1","deviceName":"maestro-device","name":"bootstrap-agent"}')"
  REQ_ID2="$(printf '%s' "${APPLY2}" | jq_get 'd.get("requestId","")')"
  [ "${REQ_ID2}" = "${REQ_ID}" ] && ok "S17b 幂等重放返回同一申请" || bad "S17b 幂等重放 requestId 漂移（${REQ_ID} → ${REQ_ID2}）"

  # status 不含明文
  ST="$(curl -s "${BOOT}/mcp/control/keys/status/${REQ_ID}")"
  case "${ST}" in
    *sk-mcp-*) bad "S18 status 泄露明文：${ST}" ;;
    *'"ok":true'*) ok "S18 status 安全（无明文）" ;;
    *) bad "S18 status 异常：${ST}" ;;
  esac

  # claim 一次性
  CL="$(curl -s -X POST "${BOOT}/mcp/control/keys/claim" -H 'Content-Type: application/json' \
    -d "{\"requestId\":\"${REQ_ID}\",\"claimToken\":\"${CLAIM_TOKEN}\"}")"
  BOOT_KEY="$(printf '%s' "${CL}" | jq_get 'd.get("apiKey","")')"
  case "${BOOT_KEY}" in
    sk-mcp-*) ok "S19 claim 一次性领取明文凭证" ;;
    *) bad "S19 claim 异常：${CL}" ;;
  esac
  CL2="$(curl -s -X POST "${BOOT}/mcp/control/keys/claim" -H 'Content-Type: application/json' \
    -d "{\"requestId\":\"${REQ_ID}\",\"claimToken\":\"${CLAIM_TOKEN}\"}")"
  case "${CL2}" in
    *already_claimed*) ok "S19b claim 不可重放（already_claimed）" ;;
    *) bad "S19b claim 可重放：${CL2}" ;;
  esac
else
  bad "S17~S19 引导面实例未就绪"
fi

# S21 非法请求级能力域 → 400 + JSON-RPC -32602 + 带合法清单的文案
#   ★ 契约来源：既有 gateway.js:519-527（逐字文案）⇒ 在**真实进程**上验，
#     而不只靠 Go 单测。
log "== S21~S22 请求级能力域契约"
code="$(rpc "${BASE_URL}" '/mcp?capability=bogus' "${DEMO_KEY}" '{"jsonrpc":"2.0","id":20,"method":"tools/list"}')"
bad_code="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
bad_msg="$(body_get | jq_get 'd.get("error",{}).get("message","")')"
if [ "${code}" = "400" ] && [ "${bad_code}" = "-32602" ] && case "${bad_msg}" in
     *"Unknown capability: bogus. Valid: code, memory, agent, wiki, apikey, federation, reasoning"*) true ;;
     *) false ;;
   esac; then
  ok "S21 非法能力域 → 400 + -32602 + 文案含合法清单"
else
  bad "S21 非法能力域 → code=${code} 码=${bad_code} msg='${bad_msg}'"
fi

# S22 合法请求级能力域 → 覆盖适配器默认，且注入头是 **JSON 数组字符串**
#   ★ 观察点是**上游实际收到的头**（内置示例上游把它回显在结果里）⇒
#     证明「请求级覆盖生效」+「头格式与既有 JSON.stringify([...]) 逐字一致」。
code="$(rpc "${BASE_URL}" '/mcp?capability=code' "${DEMO_KEY}" '{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"demo__search_memory","arguments":{"q":"probe"}}}')"
up_cap="$(body_get | jq_get 'd.get("result",{}).get("capability","")')"
up_tools="$(body_get | jq_get 'd.get("result",{}).get("capabilityTools","")')"
# 头必须是可被 JSON.parse 的**数组字符串**（既有实现是 JSON.stringify([...])）
tools_parsed="$(printf '%s' "${up_tools}" | python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
except Exception:
    print(""); raise SystemExit(0)
print(",".join(d) if isinstance(d,list) else "")' 2>/dev/null)"
if [ "${code}" = "200" ] && [ "${up_cap}" = "code" ]; then
  ok "S22 请求级能力域覆盖适配器默认（上游收到 X-MCP-Capability=${up_cap}）"
else
  bad "S22 请求级能力域 → code=${code} 上游收到 '${up_cap}'（期望 code）"
fi
case "${tools_parsed}" in
  code_generate,*) ok "S22b 能力工具头是 JSON 数组字符串（可 JSON.parse，首项 code_generate）" ;;
  *) bad "S22b 能力工具头格式不符：'${up_tools}'" ;;
esac

# S20 工具级强制力运行时探针（三档结论）
#
# ★ 移植既有 tools/mcp-gateway 的 scripts/probe-tool-allow-enforcement.js：
#   这道门测的是**结果**（对真实路径真打一次不该被允许的调用），**不测拓扑**。
#   三档：deny 工具 403 且 allow 工具 200 ⇒ 执法在生效（exit 0）；
#         deny 工具放行 ⇒ 执法没生效（exit 1）；其余 ⇒ 未验证（exit 2）。
log "== S20 工具级强制力探针"
PROBE_KEY="$(curl -s -X POST "${BASE_URL}/mcp/control/keys" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"default","name":"maestro-probe","scopes":["mcp.read","mcp.execute"],"allowed_adapters":["demo"],"allowed_tools":["demo__search_memory"]}' \
  | jq_get 'd.get("secret","")')"
if [ -z "${PROBE_KEY}" ]; then
  bad "S20 未能签发探针凭证"
else
  if MCP_API_KEY="${PROBE_KEY}" bash "${ROOT}/tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh" \
      --base "${BASE_URL}" --path /mcp \
      --allow-tool demo__search_memory --allow-args '{"q":"probe"}' \
      --deny-tool demo__add_memory --deny-args '{"text":"probe"}' \
      >"${OUT}/probe-tool-allow.log" 2>&1; then
    prc=0
  else
    prc=$?
  fi
  case "${prc}" in
    0) ok "S20 工具级强制力探针 → 执法在生效（exit 0）" ;;
    1) bad "S20 工具级强制力探针 → 执法没生效（exit 1，见 probe-tool-allow.log）" ;;
    *) bad "S20 工具级强制力探针 → 未验证（exit ${prc}，见 probe-tool-allow.log）" ;;
  esac
fi

# S23 请求体超限 → 413 + -32004（既有 gateway.js:295 的字面契约）
#   ★ 用独立实例把上限调到 64 字节 ⇒ 不必真的发 1MB。
log "== S23 请求体超限"
BODY_PID="$(start_gateway "${BODY_PORT}" 120 false "${OUT}/gateway-body.log" 64)"
if wait_ready "http://127.0.0.1:${BODY_PORT}"; then
  BODY_KEY="$(grep -m1 '^DEMO_API_KEY=' "${OUT}/gateway-body.log" | cut -d= -f2-)"
  code="$(rpc "http://127.0.0.1:${BODY_PORT}" /mcp "${BODY_KEY}" '{"jsonrpc":"2.0","id":30,"method":"tools/list","params":{"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}')"
  big_code="$(body_get | jq_get 'd.get("error",{}).get("code",0)')"
  if [ "${code}" = "413" ] && [ "${big_code}" = "-32004" ]; then
    ok "S23 请求体超限 → 413（JSON-RPC 码 -32004，与既有一致）"
  else
    bad "S23 请求体超限 → ${code} 码=${big_code}（期望 413 / -32004）"
  fi
else
  bad "S23 请求体上限实例未就绪"
fi

# S24 遗留旧名指标族可见（运维按旧名判断「收口生不生效」）
METRICS="$(curl -s --max-time 5 "${BASE_URL}/metrics" || true)"
missing=""
for m in tool_allow_checks_total tool_allow_not_enforced_total tool_allow_denied_total \
         tool_allow_lookup_errors_total tool_allow_lookup_misses_total \
         bindings_checks_total bindings_denied_total bindings_inactive_total \
         bindings_lookup_errors_total bindings_lookup_misses_total \
         keys_apply_total keys_claim_total keys_status_total; do
  case "${METRICS}" in
    *"${m}"*) : ;;
    *) missing="${missing} ${m}" ;;
  esac
done
if [ -z "${missing}" ]; then
  ok "S24 遗留旧名指标族全部出现在 /metrics（13 族）"
else
  bad "S24 /metrics 缺指标族：${missing}"
fi
# ★ 光「族名在」不够（计数为 0 也照样出现）⇒ 断言关键几个**确实动过**：
#   tool_allow_checks_total      —— 允许表判定发生过
#   tool_allow_not_enforced_total —— 存在「无允许表 ⇒ 执法跳过」的 key（= 收口没生效的信号）
#   bindings_checks_total        —— 绑定闸判定发生过
METRICS_VAL="$(curl -s --max-time 5 "${BASE_URL}/metrics" || true)"
mv_get() { printf '%s' "${METRICS_VAL}" | awk -v m="$1" '$1==m {print $2; exit}'; }
tc="$(mv_get tool_allow_checks_total)"
tn="$(mv_get tool_allow_not_enforced_total)"
bc="$(mv_get bindings_checks_total)"
if [ -n "${tc}" ] && [ "${tc%%.*}" -gt 0 ] 2>/dev/null && \
   [ -n "${tn}" ] && [ "${tn%%.*}" -gt 0 ] 2>/dev/null && \
   [ -n "${bc}" ] && [ "${bc%%.*}" -gt 0 ] 2>/dev/null; then
  ok "S24b 旧名计数器确实在自增（checks=${tc} not_enforced=${tn} bindings=${bc}）"
else
  bad "S24b 旧名计数器未自增（checks='${tc}' not_enforced='${tn}' bindings='${bc}'）"
fi

# ── 4. S25~S31：业务 key 按 scope 访问控制面 ─────────────────────────────────
#
# ★ 目标（用户原话）：「在网关中有新的 key 来对 mcp 进行注册、管理与统计
#   分析等能力」—— 控制面不再只认管理令牌，带 scope 的业务 key 也能按
#   能力位进控制面。
#
#   能力位：mcp.register（注册面）/ mcp.manage（管理面）/ mcp.analyze（统计
#   分析面）/ mcp.topology（内部拓扑面，/health 单独口径）。
#
# ★ 失败口径：凭据无效 ⇒ 401；身份有效但 scope 不足 ⇒ 403（体里带
#   required_scope）；未配置管理令牌且无从判定身份 ⇒ 503。
ctl() { # ctl <base-url> <method> <path> <key> [extra-header]
  local base="$1" method="$2" path="$3" key="$4" extra="${5:-}"
  local args=(-s -o "${OUT}/.body" -w '%{http_code}' -X "${method}" "${base}${path}"
              -H 'Content-Type: application/json')
  [ -n "${key}" ] && args+=(-H "Authorization: Bearer ${key}")
  [ -n "${extra}" ] && args+=(-H "${extra}")
  curl "${args[@]}"
}
ctl_b() { # ctl_b <base-url> <method> <path> <key> <body> [extra-header]
  local base="$1" method="$2" path="$3" key="$4" body="$5" extra="${6:-}"
  local args=(-s -o "${OUT}/.body" -w '%{http_code}' -X "${method}" "${base}${path}"
              -H 'Content-Type: application/json')
  [ -n "${key}" ] && args+=(-H "Authorization: Bearer ${key}")
  [ -n "${extra}" ] && args+=(-H "${extra}")
  args+=(-d "${body}")
  curl "${args[@]}"
}
issue_key() { # issue_key <base-url> <name> <scopes-json-array>
  curl -s -X POST "${1}/mcp/control/keys" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
    -H 'Content-Type: application/json' \
    -d "{\"tenant_id\":\"default\",\"name\":\"${2}\",\"scopes\":${3}}" \
    | jq_get 'd.get("secret","")'
}

log "== S25~S31 业务 key 按 scope 访问控制面"

# S25 注册面：mcp.register 的 key 能注册/查看，不能改删（改删属管理面）
REGISTER_KEY="$(issue_key "${BASE_URL}" "maestro-register" '["mcp.register"]')"
if [ -z "${REGISTER_KEY}" ]; then
  bad "S25 未能签发 mcp.register 权限的凭证"
else
  code="$(ctl_b "${BASE_URL}" POST /mcp/control/adapters "${REGISTER_KEY}" \
    '{"name":"scope-reg","endpoint":"http://127.0.0.1:8431","capability":"code","enabled":true}')"
  [ "${code}" = "201" ] && ok "S25 mcp.register 的 key 注册适配器 → 201" \
    || bad "S25 mcp.register 注册适配器 → ${code}（期望 201）"
  code="$(ctl "${BASE_URL}" GET /mcp/control/adapters "${REGISTER_KEY}")"
  [ "${code}" = "200" ] && ok "S25b mcp.register 的 key 查看注册面 → 200" \
    || bad "S25b mcp.register 查看注册面 → ${code}（期望 200）"
  code="$(ctl "${BASE_URL}" DELETE /mcp/control/adapters/scope-reg "${REGISTER_KEY}")"
  req_scope="$(body_get | jq_get 'd.get("required_scope","")')"
  if [ "${code}" = "403" ] && [ "${req_scope}" = "mcp.manage" ]; then
    ok "S25c mcp.register 的 key 改删被拒 → 403（required_scope=mcp.manage）"
  else
    bad "S25c mcp.register 改删 → ${code} required_scope='${req_scope}'（期望 403 / mcp.manage）"
  fi
fi

# S26 管理面：mcp.manage 的 key 能改删注册项、能签发/吊销凭证
MANAGE_KEY="$(issue_key "${BASE_URL}" "maestro-manage" '["mcp.manage","mcp.read","mcp.execute"]')"
if [ -z "${MANAGE_KEY}" ]; then
  bad "S26 未能签发 mcp.manage 权限的凭证"
else
  curl -s -o /dev/null -X POST "${BASE_URL}/mcp/control/adapters" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
    -H 'Content-Type: application/json' \
    -d '{"name":"scope-mgd","endpoint":"http://127.0.0.1:8432","enabled":true}'
  code="$(ctl "${BASE_URL}" DELETE /mcp/control/adapters/scope-mgd "${MANAGE_KEY}")"
  [ "${code}" = "204" ] && ok "S26 mcp.manage 的 key 删除适配器 → 204" \
    || bad "S26 mcp.manage 删除适配器 → ${code}（期望 204）"
fi

# S27 统计分析面：业务 key 的租户审计与管理令牌的进程指标分开验收
ANALYZE_KEY="$(issue_key "${BASE_URL}" "maestro-analyze" '["mcp.analyze"]')"
if [ -z "${ANALYZE_KEY}" ]; then
  bad "S27 未能签发 mcp.analyze 权限的凭证"
else
  code="$(ctl "${BASE_URL}" GET "/mcp/control/audit?limit=50" "${ANALYZE_KEY}")"
  events="$(body_get | jq_get 'len(d.get("events",[]))')"
  if [ "${code}" = "200" ]; then
    ok "S27 mcp.analyze 的 key 查审计 → 200（${events} 条事件）"
  else
    bad "S27 mcp.analyze 查审计 → ${code}（期望 200）"
  fi
  code="$(ctl "${BASE_URL}" GET /mcp/control/stats "${ANALYZE_KEY}")"
  fams="$(body_get | jq_get 'len(d.get("families",[]))')"
  scope="$(body_get | jq_get 'd.get("scope","")')"
  audit_ok="$(body_get | jq_get 'd.get("audit",{}).get("total",0)>0 and isinstance(d.get("audit",{}).get("by_tool"),dict)')"
  if [ "${code}" = "200" ] && [ "${fams}" = "0" ] && [ "${scope}" = "tenant_audit_only" ] && [ "${audit_ok}" = "True" ]; then
    ok "S27b mcp.analyze 的 key → 200（tenant_audit_only、进程指标隔离、租户审计可用）"
  else
    bad "S27b 租户统计 → ${code} families='${fams}' scope='${scope}' audit_ok='${audit_ok}'"
  fi
  code="$(ctl "${BASE_URL}" GET /mcp/control/stats "${ADMIN_TOKEN}")"
  fams="$(body_get | jq_get 'len(d.get("families",[]))')"
  if [ "${code}" = "200" ] && [ "${fams%%.*}" -gt 0 ] 2>/dev/null; then
    ok "S27b-admin 管理令牌保留完整进程指标 → 200（${fams} 个指标族）"
  else
    bad "S27b-admin 进程指标 → ${code} families='${fams}'（期望200且非空）"
  fi
  # /health 含内部拓扑 ⇒ 需要单独的 mcp.topology
  code="$(ctl "${BASE_URL}" GET /mcp/control/health "${ANALYZE_KEY}")"
  req_scope="$(body_get | jq_get 'd.get("required_scope","")')"
  if [ "${code}" = "403" ] && [ "${req_scope}" = "mcp.topology" ]; then
    ok "S27c /health 单独口径：仅有 mcp.analyze ⇒ 403（required_scope=mcp.topology）"
  else
    bad "S27c /health 口径 → ${code} required_scope='${req_scope}'（期望 403 / mcp.topology）"
  fi
fi

# S28 拓扑面：带 mcp.topology 的 key 能看 /health
TOPO_KEY="$(issue_key "${BASE_URL}" "maestro-topology" '["mcp.analyze","mcp.topology"]')"
if [ -z "${TOPO_KEY}" ]; then
  bad "S28 未能签发 mcp.topology 权限的凭证"
else
  code="$(ctl "${BASE_URL}" GET /mcp/control/health "${TOPO_KEY}")"
  [ "${code}" = "200" ] && ok "S28 带 mcp.topology 的 key 读 /health → 200" \
    || bad "S28 带 mcp.topology 读 /health → ${code}（期望 200）"
fi

# S29 权限放大防护：签不出比自己权限更大的 key
if [ -n "${MANAGE_KEY}" ]; then
  code="$(ctl_b "${BASE_URL}" POST /mcp/control/keys "${MANAGE_KEY}" \
    '{"tenant_id":"default","name":"escalate","scopes":["mcp.analyze"]}')"
  reason="$(body_get | jq_get 'd.get("reason","")')"
  if [ "${code}" = "403" ] && [ "${reason}" = "scope_escalation" ]; then
    ok "S29 权限放大被挡：mcp.manage 签 mcp.analyze 的 key → 403（scope_escalation）"
  else
    bad "S29 权限放大 → ${code} reason='${reason}'（期望 403 / scope_escalation）"
  fi
  code="$(ctl_b "${BASE_URL}" POST /mcp/control/keys "${MANAGE_KEY}" \
    '{"tenant_id":"default","name":"delegate","scopes":["mcp.manage"]}')"
  [ "${code}" = "201" ] && ok "S29b 签出不超出自身的 key → 201（委派可用）" \
    || bad "S29b 签出不超出自身的 key → ${code}（期望 201）"
fi
if [ -n "${REGISTER_KEY}" ]; then
  code="$(ctl_b "${BASE_URL}" POST /mcp/control/keys "${REGISTER_KEY}" \
    '{"tenant_id":"default","name":"reg-sign","scopes":["mcp.register"]}')"
  req_scope="$(body_get | jq_get 'd.get("required_scope","")')"
  if [ "${code}" = "403" ] && [ "${req_scope}" = "mcp.manage" ]; then
    ok "S29c mcp.register 的 key 无权签发凭证 → 403（required_scope=mcp.manage）"
  else
    bad "S29c mcp.register 签发凭证 → ${code} required_scope='${req_scope}'（期望 403 / mcp.manage）"
  fi
fi

# S30 无控制面 scope 的 key 一律被拒（且与 401 区分开：身份有效 ⇒ 403）
PLAIN_KEY="$(issue_key "${BASE_URL}" "maestro-plain" '["mcp.read","mcp.execute"]')"
if [ -n "${PLAIN_KEY}" ]; then
  code="$(ctl "${BASE_URL}" GET /mcp/control/tools "${PLAIN_KEY}")"
  reason="$(body_get | jq_get 'd.get("reason","")')"
  if [ "${code}" = "403" ] && [ "${reason}" = "scope_denied" ]; then
    ok "S30 无控制面 scope 的 key → 403（scope_denied，与 401 区分）"
  else
    bad "S30 无 scope 的 key → ${code} reason='${reason}'（期望 403 / scope_denied）"
  fi
fi

# S31 租户收口：业务 key 不能跨租户操作
TENANT_KEY="$(curl -s -X POST "${BASE_URL}/mcp/control/keys" -H "X-Admin-Token: ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"tenant-a","name":"maestro-tenant","scopes":["mcp.register"]}' \
  | jq_get 'd.get("secret","")')"
if [ -n "${TENANT_KEY}" ]; then
  code="$(ctl "${BASE_URL}" GET /mcp/control/adapters "${TENANT_KEY}" "X-Tenant-Id: tenant-b")"
  reason="$(body_get | jq_get 'd.get("reason","")')"
  if [ "${code}" = "403" ] && [ "${reason}" = "tenant_mismatch" ]; then
    ok "S31 跨租户访问被拒 → 403（tenant_mismatch）"
  else
    bad "S31 跨租户访问 → ${code} reason='${reason}'（期望 403 / tenant_mismatch）"
  fi
fi

# ── 5. L3：Maestro 浏览器 flow ──────────────────────────────────────────────
if [ "${SKIP_MAESTRO:-0}" != "1" ]; then
  log "== L3: Maestro 浏览器 flow（运营看板）"
  if ! command -v maestro >/dev/null 2>&1; then
    bad "L3 PATH 里没有 maestro（curl -Ls https://get.maestro.mobile.dev | bash）"
  else
    DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
    CHROME_PROFILE="${MAESTRO_CHROME_PROFILE:-/tmp/maestro-local-chrome-profile}"
    find_chrome() {
      if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
      if command -v chromium >/dev/null 2>&1; then command -v chromium; return; fi
      if command -v google-chrome >/dev/null 2>&1; then command -v google-chrome; return; fi
      for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
               "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
        [ -x "${c}" ] && { printf '%s' "${c}"; return; }
      done
      printf ''
    }
    CHROME_BIN="$(find_chrome)"
    if [ -z "${CHROME_BIN}" ]; then
      bad "L3 找不到 Chrome（MAESTRO_CHROME_BIN 可指定）"
    else
      if ! curl -s --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
        mkdir -p "${CHROME_PROFILE}"
        nohup "${CHROME_BIN}" --headless=new --remote-debugging-port="${DRIVER_PORT}" \
          --user-data-dir="${CHROME_PROFILE}" --no-first-run --no-default-browser-check \
          --disable-gpu about:blank >"${OUT}/chrome.log" 2>&1 &
        for _ in $(seq 1 30); do
          curl -s --max-time 2 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1 && break
          sleep 1
        done
      fi
      set +e
      maestro test --driver-host-port "${DRIVER_PORT}" --headless --no-ansi \
        --test-output-dir "${OUT}/maestro" \
        -e "BASE_URL=${BASE_URL}" -e "MCP_ADMIN_TOKEN=${ADMIN_TOKEN}" \
        "${ROOT}/maestro/flows/10-mcp-gateway.yaml" >"${OUT}/maestro.log" 2>&1
      rc=$?
      set -e
      failed="$(grep -c 'FAILED' "${OUT}/maestro.log" || true)"
      if [ "${rc}" -eq 0 ] && [ "${failed}" -eq 0 ]; then
        ok "L3 Maestro flow 全绿（rc=0，FAILED=0）"
      else
        bad "L3 Maestro flow 失败 rc=${rc} FAILED=${failed}（见 maestro.log）"
      fi
    fi
  fi
fi

# ── 6. 汇总 ─────────────────────────────────────────────────────────────────
{
  echo "# MCP 网关 Maestro 验证报告"
  echo
  echo "- 时间：$(date '+%Y-%m-%d %H:%M:%S')"
  echo "- 目标：llm-gateway-go 内置 MCP 服务网关（mcp/ 包 + cmd/mcp-gateway）"
  echo "- 用例：L1 Go 测试 + L2 API 场景（S01~S31）+ L3 Maestro 浏览器 flow"
  echo
  echo "## 结论"
  echo
  echo "- 通过：${pass}"
  echo "- 失败：${fail}"
  echo
  if [ "${fail}" -gt 0 ]; then
    echo "## 失败用例"
    echo
    for c in "${FAILED_CASES[@]}"; do echo "- ${c}"; done
    echo
  fi
  echo "## 用例明细（含读数）"
    echo
  for c in "${ALL_CASES[@]}"; do echo "- ${c}"; done
  echo
  echo "## 产物"
  echo
  echo "- go-test.log：L1 Go 测试输出"
  echo "- gateway-*.log：网关进程日志"
  echo "- maestro.log：L3 Maestro CLI 输出"
} >"${OUT}/summary.md"

log ""
log "================ 汇总 ================"
log "通过 ${pass} / 失败 ${fail}"
log "报告：${OUT}/summary.md"
[ "${fail}" -eq 0 ] || exit 1
