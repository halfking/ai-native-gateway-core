#!/usr/bin/env bash
#
# 提示词优化模块 —— **真实链路** 端到端验证脚本
#
# 与 scripts/test-prompt-optimizer.sh 的区别（不是重复，是覆盖不同的面）：
#   test-prompt-optimizer.sh        Docker + memora **桩**，验服务契约与并发
#   test-prompt-optimizer-real.sh   **真实 Memora 实例** + **真实内部小模型**
#                                   + **真实 Hook 契约端点**，验闭环
#
# 真实链路上验的东西（桩里验不到）：
#   1. service JWT + project scope 能真的进 memora（v2 fail-closed 组）
#   2. 优化产出真的落进 memora，且能被检索回来（历史闭环）
#   3. 网关内置 Hook 的契约端点 /api/v1/prompts/optimize 真的能返回合法响应
#   4. 内部小模型（OpenAI 兼容端点）真的能被优化器调用
#
# 前置：
#   - memora 实例在 MEMORA_BASE_URL（默认 http://127.0.0.1:12113）
#   - 内部小模型在 OLLAMA_BASE_URL（默认 http://127.0.0.1:11434/v1）
#   - 密钥：环境变量 MEMORA_JWT_SECRET，或从 memora-server 容器自动读取
#
# 用法：./scripts/test-prompt-optimizer-real.sh

set -uo pipefail

# ★ 专用 Go 构建缓存（★ scripts/lib/gocache.sh）：编译型 go 命令的产物不许落进用户全局缓存。
export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

MEMORA_BASE="${MEMORA_BASE_URL:-http://127.0.0.1:12113}"
OLLAMA_BASE="${OLLAMA_BASE_URL:-http://127.0.0.1:11434/v1}"
PROJECT_ID="${MEMORA_PROJECT_ID:-prompt-optimizer}"
ACTOR_ID="${MEMORA_ACTOR_ID:-local-ops}"
TENANT_ID="${MEMORA_TENANT_ID:-default}"
SERVICE_PORT="${SERVICE_PORT:-18091}"
SERVICE_URL="http://127.0.0.1:${SERVICE_PORT}"
OLLAMA_MODEL="${OLLAMA_MODEL:-qwen2.5:1.5b}"
WORK_DIR="$(mktemp -d)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
log_info()  { echo -e "${BLUE}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_ok()    { echo -e "${GREEN}  ✓${NC} $1"; }

PASS_COUNT=0
FAIL_COUNT=0
SERVICE_PID=""

assert_contains() {
  local haystack="$1" needle="$2" desc="$3"
  if [[ "${haystack}" == *"${needle}"* ]]; then
    PASS_COUNT=$((PASS_COUNT + 1)); log_ok "${desc}"
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    log_error "${desc} —— 期望包含 '${needle}'，实际: ${haystack:0:400}"
  fi
}

assert_not_contains() {
  local haystack="$1" needle="$2" desc="$3"
  if [[ "${haystack}" != *"${needle}"* ]]; then
    PASS_COUNT=$((PASS_COUNT + 1)); log_ok "${desc}"
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    log_error "${desc} —— 不应包含 '${needle}'，实际: ${haystack:0:400}"
  fi
}

assert_status() {
  local got="$1" want="$2" desc="$3"
  if [[ "${got}" == "${want}" ]]; then
    PASS_COUNT=$((PASS_COUNT + 1)); log_ok "${desc}"
  else
    FAIL_COUNT=$((FAIL_COUNT + 1)); log_error "${desc} —— 期望 ${want}，实际 ${got}"
  fi
}

cleanup() {
  if [[ -n "${SERVICE_PID}" ]] && kill -0 "${SERVICE_PID}" 2>/dev/null; then
    kill "${SERVICE_PID}" 2>/dev/null
    wait "${SERVICE_PID}" 2>/dev/null
  fi
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# ── 密钥获取（只存在内存里，不落盘、不打印） ─────────────────────────
resolve_secret() {
  if [[ -n "${MEMORA_JWT_SECRET:-}" ]]; then
    echo "${MEMORA_JWT_SECRET}"; return 0
  fi
  if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'memora-server'; then
    docker inspect memora-server --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
      | grep '^JWT_SECRET=' | head -1 | cut -d= -f2-
    return 0
  fi
  return 1
}

# mint_jwt 用 HS256 签一个 token。
# 参数：secret iss aud sub tenant actor scope
mint_jwt() {
  python3 - "$@" <<'PY'
import base64, hashlib, hmac, json, sys, time
secret, iss, aud, sub, tenant, actor, scope = sys.argv[1:8]
def b64u(raw): return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()
payload = {
    "iss": iss, "aud": aud, "sub": sub,
    "tenant_id": tenant, "actor_id": actor, "actor_type": "service",
    "scope": scope, "iat": int(time.time()), "exp": int(time.time()) + 3600,
}
h = b64u(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
p = b64u(json.dumps(payload, separators=(",", ":")).encode())
sig = hmac.new(secret.encode(), f"{h}.{p}".encode(), hashlib.sha256).digest()
print(f"{h}.{p}.{b64u(sig)}")
PY
}

# ── 前置检查 ───────────────────────────────────────────────────────
log_info "前置检查"
if ! command -v curl >/dev/null 2>&1; then
  log_error "缺少 curl"; exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  log_error "缺少 python3（用于签 JWT）"; exit 1
fi

MEMORA_HEALTH="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${MEMORA_BASE}/healthz" 2>/dev/null)"
log_info "memora /healthz = ${MEMORA_HEALTH}"

OLLAMA_MODELS="$(curl -s --max-time 10 "${OLLAMA_BASE}/models" 2>/dev/null)"
if [[ -z "${OLLAMA_MODELS}" ]]; then
  log_error "内部小模型端点不可达：${OLLAMA_BASE}"; exit 1
fi
if [[ "${OLLAMA_MODELS}" != *"${OLLAMA_MODEL}"* ]]; then
  log_warn "模型 ${OLLAMA_MODEL} 不在端点列表里，实际: ${OLLAMA_MODELS:0:200}"
fi
log_info "内部小模型端点可达：${OLLAMA_BASE}（模型 ${OLLAMA_MODEL}）"

SECRET="$(resolve_secret)"
if [[ -z "${SECRET}" ]]; then
  log_error "拿不到 memora 密钥（设置 MEMORA_JWT_SECRET 或保证 memora-server 容器在跑）"; exit 1
fi
log_info "已取得 memora 密钥（长度 ${#SECRET}，不打印明文）"

# service JWT（v2 fail-closed 组）+ user JWT（legacy 建 project）
SVC_TOKEN="$(mint_jwt "${SECRET}" "memora" "memora-api" "service:local-auth:${ACTOR_ID}" \
  "${TENANT_ID}" "${ACTOR_ID}" "memory.read memory.write knowledge.read knowledge.write")"
USER_TOKEN="$(python3 - "${SECRET}" "${ACTOR_ID}" "${TENANT_ID}" <<'PY'
import base64, hashlib, hmac, json, sys, time
secret, sub, tenant = sys.argv[1:4]
def b64u(raw): return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()
h = b64u(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
p = b64u(json.dumps({"sub": sub, "user_id": sub, "tenant_id": tenant, "exp": int(time.time()) + 3600}, separators=(",", ":")).encode())
sig = hmac.new(secret.encode(), f"{h}.{p}".encode(), hashlib.sha256).digest()
print(f"{h}.{p}.{b64u(sig)}")
PY
)"

# ── 确保 project 存在 ─────────────────────────────────────────────
log_info "确保 memora project 存在：${PROJECT_ID}"
curl -s -o /dev/null --max-time 20 -X POST "${MEMORA_BASE}/api/projects" \
  -H "Authorization: Bearer ${USER_TOKEN}" -H "Content-Type: application/json" \
  -d "{\"project_id\":\"${PROJECT_ID}\",\"name\":\"Prompt Optimizer Real E2E\"}"

# ── 启动服务 ──────────────────────────────────────────────────────
log_info "构建并启动 prompt-optimizer 服务（端口 ${SERVICE_PORT}）"
if ! go build -o "${WORK_DIR}/prompt-optimizer" ./cmd/prompt-optimizer; then
  log_error "构建失败"; exit 1
fi

PROMPT_OPTIMIZER_MEMORA_BASE_URL="${MEMORA_BASE}" \
PROMPT_OPTIMIZER_MEMORA_JWT_SECRET="${SECRET}" \
PROMPT_OPTIMIZER_MEMORA_PROJECT_ID="${PROJECT_ID}" \
PROMPT_OPTIMIZER_MEMORA_ACTOR_ID="${ACTOR_ID}" \
PROMPT_OPTIMIZER_MEMORA_TENANT_ID="${TENANT_ID}" \
LLM_BASE_URL="${OLLAMA_BASE}" \
LLM_API_KEY="" \
EVALUATOR_MODEL="${OLLAMA_MODEL}" \
"${WORK_DIR}/prompt-optimizer" -http ":${SERVICE_PORT}" -log-level info \
  >"${WORK_DIR}/service.log" 2>&1 &
SERVICE_PID=$!

for _ in $(seq 1 40); do
  if curl -s -o /dev/null --max-time 2 "${SERVICE_URL}/health"; then break; fi
  sleep 0.5
done
if ! curl -s -o /dev/null --max-time 3 "${SERVICE_URL}/health"; then
  log_error "服务没起来，日志："; sed -n '1,60p' "${WORK_DIR}/service.log"; exit 1
fi
log_ok "服务已启动"

# ── 断言 ──────────────────────────────────────────────────────────
log_info "断言开始"

# 1. 健康检查
HEALTH="$(curl -s --max-time 5 "${SERVICE_URL}/health")"
assert_contains "${HEALTH}" '"status":"healthy"' "健康检查返回 healthy"

# 2. 网关 Hook 契约端点
MARK="real-e2e-$$-$(date +%s)"
HOOK_BODY="$(curl -s -w '\n%{http_code}' --max-time 60 -X POST "${SERVICE_URL}/api/v1/prompts/optimize" \
  -H "Content-Type: application/json" \
  -d "{\"tenant_id\":\"t1\",\"model\":\"${OLLAMA_MODEL}\",\"prompts\":[{\"role\":\"system\",\"content\":\"你是一个严谨的助手。${MARK}\"},{\"role\":\"user\",\"content\":\"把下面三条商品转成 JSON 数组给我看看：苹果 3.5、香蕉 2、橙子 4\"}],\"options\":{}}")"
HOOK_CODE="$(echo "${HOOK_BODY}" | tail -1)"
HOOK_JSON="$(echo "${HOOK_BODY}" | sed '$d')"
assert_status "${HOOK_CODE}" "200" "Hook 契约端点返回 200"
assert_contains "${HOOK_JSON}" '"optimization_id":"opt_' "响应含 optimization_id（opt_ 前缀）"
assert_contains "${HOOK_JSON}" '"cache_hit":false' "首次调用 cache_hit=false"
assert_contains "${HOOK_JSON}" '"original_tokens":' "响应含 original_tokens"
assert_contains "${HOOK_JSON}" '"optimized_tokens":' "响应含 optimized_tokens"
assert_contains "${HOOK_JSON}" '"latency_ms":' "响应含 latency_ms"
assert_contains "${HOOK_JSON}" '"role":"system"' "响应保留 system 角色"
assert_contains "${HOOK_JSON}" '"changed":' "响应含 changed 标志"

# 3. 缓存命中
HOOK_BODY2="$(curl -s -w '\n%{http_code}' --max-time 60 -X POST "${SERVICE_URL}/api/v1/prompts/optimize" \
  -H "Content-Type: application/json" \
  -d "{\"tenant_id\":\"t1\",\"model\":\"${OLLAMA_MODEL}\",\"prompts\":[{\"role\":\"system\",\"content\":\"你是一个严谨的助手。${MARK}\"},{\"role\":\"user\",\"content\":\"把下面三条商品转成 JSON 数组给我看看：苹果 3.5、香蕉 2、橙子 4\"}],\"options\":{}}")"
HOOK_JSON2="$(echo "${HOOK_BODY2}" | sed '$d')"
assert_contains "${HOOK_JSON2}" '"cache_hit":true' "相同请求第二次 cache_hit=true"

# 4. 错误路径
ERR_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "${SERVICE_URL}/api/v1/prompts/optimize" \
  -H "Content-Type: application/json" -d '{"prompts":[]}')"
assert_status "${ERR_CODE}" "400" "空 prompts 返回 400"

GET_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${SERVICE_URL}/api/v1/prompts/optimize")"
assert_status "${GET_CODE}" "405" "GET 方法返回 405"

ERR_CODE2="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "${SERVICE_URL}/api/v1/optimize" \
  -H "Content-Type: application/json" -d '{"prompt":""}')"
assert_status "${ERR_CODE2}" "400" "空 prompt 返回 400"

# 5. 优化端点 + memora 历史
OPT_JSON="$(curl -s --max-time 120 -X POST "${SERVICE_URL}/api/v1/optimize" \
  -H "Content-Type: application/json" \
  -d "{\"prompt\":\"把下面三条商品转成 JSON 数组给我看看：苹果 3.5、香蕉 2、橙子 4 ${MARK}\",\"task_type\":\"json_generation\",\"domain\":\"technical\",\"use_memora\":true,\"inject_harness\":true}")"
assert_contains "${OPT_JSON}" '"optimized":' "优化端点返回 optimized 字段"
assert_contains "${OPT_JSON}" '"harness_elements"' "优化端点返回 harness_elements 字段"
assert_contains "${OPT_JSON}" '"context_sources"' "优化端点返回 context_sources 字段"

# 6. 结构质量对比端点
CMP_JSON="$(curl -s --max-time 30 -X POST "${SERVICE_URL}/api/v1/compare" \
  -H "Content-Type: application/json" \
  -d '{"prompt_a":"你好","prompt_b":"请严格只输出 JSON 数组，元素包含 name 与 price 字段，不要添加任何解释文字。","mode":"quality"}')"
assert_contains "${CMP_JSON}" '"mode":"quality"' "compare 返回 mode=quality"
assert_contains "${CMP_JSON}" '"interpretation"' "compare 返回口径说明"

# 7. **闭环验证**：优化产出真的进了 memora，且能被检索回来
log_info "验证 Memora 闭环（写入 → 检索回读）"
sleep 2
SEARCH_JSON="$(curl -s --max-time 30 -X POST "${MEMORA_BASE}/api/v2/memories/search" \
  -H "Authorization: Bearer ${SVC_TOKEN}" -H "X-Project-ID: ${PROJECT_ID}" \
  -H "Content-Type: application/json" \
  -d "{\"query\":\"${MARK}\",\"top_k\":10,\"scope_chain\":{\"tenant_id\":\"${TENANT_ID}\",\"project_id\":\"${PROJECT_ID}\"}}")"
assert_contains "${SEARCH_JSON}" '"items"' "memora 检索返回 items"
assert_contains "${SEARCH_JSON}" "${MARK}" "memora 里能检索回本次优化产出（历史闭环成立）"

# 8. 负对照：错误的 service token 必须被拒
BAD_SEARCH="$(curl -s -w '\n%{http_code}' --max-time 20 -X POST "${MEMORA_BASE}/api/v2/memories/search" \
  -H "Authorization: Bearer not-a-jwt" -H "X-Project-ID: ${PROJECT_ID}" \
  -H "Content-Type: application/json" \
  -d "{\"query\":\"x\",\"top_k\":1,\"scope_chain\":{\"tenant_id\":\"${TENANT_ID}\",\"project_id\":\"${PROJECT_ID}\"}}")"
BAD_CODE="$(echo "${BAD_SEARCH}" | tail -1)"
if [[ "${BAD_CODE}" == "401" || "${BAD_CODE}" == "403" ]]; then
  PASS_COUNT=$((PASS_COUNT + 1)); log_ok "无效 token 被拒绝（${BAD_CODE}）"
else
  FAIL_COUNT=$((FAIL_COUNT + 1)); log_error "无效 token 应被拒，实际 ${BAD_CODE}"
fi

# 9. 负对照：跨 project 读不到
CROSS="$(curl -s --max-time 20 -X POST "${MEMORA_BASE}/api/v2/memories/search" \
  -H "Authorization: Bearer ${SVC_TOKEN}" -H "X-Project-ID: no-such-project-xyz" \
  -H "Content-Type: application/json" \
  -d "{\"query\":\"${MARK}\",\"top_k\":5,\"scope_chain\":{\"tenant_id\":\"${TENANT_ID}\",\"project_id\":\"no-such-project-xyz\"}}")"
assert_not_contains "${CROSS}" "${MARK}" "跨 project 读不到本 project 的记忆（隔离生效）"

# ── 汇总 ──────────────────────────────────────────────────────────
echo
if [[ "${FAIL_COUNT}" -eq 0 ]]; then
  log_info "✓ 全部 ${PASS_COUNT} 项断言通过（真实 Memora + 真实内部小模型）"
  exit 0
fi
log_error "✗ ${FAIL_COUNT} 项断言失败（共 $((PASS_COUNT + FAIL_COUNT)) 项）"
echo "--- 服务日志 ---"
sed -n '1,80p' "${WORK_DIR}/service.log"
exit 1
