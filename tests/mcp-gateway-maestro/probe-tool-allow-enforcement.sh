#!/usr/bin/env bash
# tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh
# —— 「工具级强制力是不是真的在生效」的三档运行时探针
#
# ★★ 为什么要有这道探针（移植自既有 tools/mcp-gateway 的
#    scripts/probe-tool-allow-enforcement.js，2026-10-23 在公网上实测出来的）：
#   本地联调能证明「执法点自己没坏」（不在允许集 ⇒ 403）。
#   但**流量走不走执法点**是**另一件事** ⇒ 这道探针测的是**结果**
#   （对真实路径真打一次不该被允许的调用，看它是不是真被拒），**不测拓扑**。
#
# ★ 三档结论（**不许**只有两档）：
#   - deny 工具 ⇒ 403 **且** allow 工具 ⇒ 200 ⇒ **执法在生效**（exit 0）
#   - deny 工具 ⇒ 放行 ⇒ **执法没生效**（exit 1）
#   - 其余（连不上 / 没给 key / 响应异常 / **allow 工具也被拒**）
#     ⇒ **未验证**（exit 2）——★ 「查不了」≠「查过了没问题」
#
# ★★ 为什么要 allow 工具这一档（正控）：
#   若 deny 工具被拒、allow 工具**也被拒**，那说明的是「这把 key 全被禁了」
#   或「这条路径根本不认这个 key」，**不是**执法在生效 ⇒ 必须两种工具一起看。
#
# ★ 退出码：0 执法在生效 / 1 执法没生效 / 2 未验证（**绝不是**「通过」）。
#
# 用法：
#   MCP_API_KEY=sk-... bash tests/mcp-gateway-maestro/probe-tool-allow-enforcement.sh \
#     --base http://127.0.0.1:8088 --path /mcp \
#     --allow-tool <允许集里的工具> --deny-tool <不在允许集里的工具> \
#     [--allow-args '{"q":"x"}'] [--deny-args '{}']
#
# ★ 入参参数（--allow-args/--deny-args）是为本实现补的：既有 Node 探针用
#   空 arguments{}，因为既有网关不做 JSON Schema 校验；本实现**会**按上游
#   工具的 inputSchema 校验参数 ⇒ 「允许集内的工具」必须给合法入参，
#   否则它会被参数校验拒掉（403），把「执法在生效」误判成「两边都被拒」。
#
# ★ 凭据只从环境读（`MCP_API_KEY`）⇒ 不进 argv ⇒ 不进进程列表与 shell 历史。
set -uo pipefail

BASE=""; PATH_PART="/mcp"; ALLOW_TOOL=""; DENY_TOOL=""; ALLOW_ARGS="{}"; DENY_ARGS="{}"

while [ $# -gt 0 ]; do
  case "$1" in
    --base)       BASE="${2:-}"; shift 2 ;;
    --path)       PATH_PART="${2:-}"; shift 2 ;;
    --allow-tool) ALLOW_TOOL="${2:-}"; shift 2 ;;
    --deny-tool)  DENY_TOOL="${2:-}"; shift 2 ;;
    --allow-args) ALLOW_ARGS="${2:-}"; [ -n "${ALLOW_ARGS}" ] || ALLOW_ARGS='{}'; shift 2 ;;
    --deny-args)  DENY_ARGS="${2:-}"; [ -n "${DENY_ARGS}" ] || DENY_ARGS='{}'; shift 2 ;;
    -h|--help)
      sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

BASE="${BASE:-${MCP_GATEWAY_BASE_URL:-}}"
PATH_PART="${PATH_PART:-${MCP_GATEWAY_PROBE_PATH:-/mcp}}"
ALLOW_TOOL="${ALLOW_TOOL:-${MCP_GATEWAY_PROBE_ALLOW_TOOL:-}}"
DENY_TOOL="${DENY_TOOL:-${MCP_GATEWAY_PROBE_DENY_TOOL:-}}"
API_KEY="${MCP_API_KEY:-}"

TMP_BODY="$(mktemp -t mcp-probe-body.XXXXXX)"
trap 'rm -f "${TMP_BODY}"' EXIT

fail_unverified() { # fail_unverified <reason> [detail]
  echo "  ★ 未验证：$1${2:+（$2）}"
  echo "    ★ 「查不了」不等于「查过了没问题」⇒ 本次**没有**得出任何结论。"
  exit 2
}

if [ -z "${BASE}" ]; then fail_unverified "no_base_url"; fi
if [ -z "${API_KEY}" ]; then fail_unverified "no_api_key"; fi
if [ -z "${ALLOW_TOOL}" ] || [ -z "${DENY_TOOL}" ]; then fail_unverified "missing_tool_names"; fi
if [ "${ALLOW_TOOL}" = "${DENY_TOOL}" ]; then fail_unverified "same_tool"; fi

# build_payload <tool> <args-json> —— 构造 tools/call 请求体。
build_payload() {
  python3 -c '
import json, sys
tool, raw = sys.argv[1], sys.argv[2]
try:
    args = json.loads(raw)
except Exception:
    args = {}
if not isinstance(args, dict):
    args = {}
print(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                  "params": {"name": tool, "arguments": args}}))' "$1" "$2"
}

# call_tool <tool> <args-json> —— 打一次 tools/call，回显 HTTP 状态码，响应体落到 TMP_BODY。
call_tool() {
  local payload
  payload="$(build_payload "$1" "$2")"
  curl -s --max-time 15 -o "${TMP_BODY}" -w '%{http_code}' \
    -X POST "${BASE}${PATH_PART}" \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${API_KEY}" \
    -d "${payload}" 2>/dev/null
}

echo "探测路径：${BASE}${PATH_PART}"

ALLOW_STATUS="$(call_tool "${ALLOW_TOOL}" "${ALLOW_ARGS}")"
if [ -z "${ALLOW_STATUS}" ] || [ "${ALLOW_STATUS}" = "000" ]; then
  fail_unverified "allow_call_failed" "连不上 ${BASE}${PATH_PART}"
fi
ALLOW_BODY="$(cat "${TMP_BODY}")"

DENY_STATUS="$(call_tool "${DENY_TOOL}" "${DENY_ARGS}")"
if [ -z "${DENY_STATUS}" ] || [ "${DENY_STATUS}" = "000" ]; then
  fail_unverified "deny_call_failed" "连不上 ${BASE}${PATH_PART}"
fi
DENY_BODY="$(cat "${TMP_BODY}")"

DENY_CODE="$(printf '%s' "${DENY_BODY}" | python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
except Exception:
    print(""); raise SystemExit(0)
err=d.get("error") or {}
print(err.get("code", ""))' 2>/dev/null)"

echo "允许集内的工具：${ALLOW_TOOL} ⇒ HTTP ${ALLOW_STATUS}"
echo "不在允许集的工具：${DENY_TOOL} ⇒ HTTP ${DENY_STATUS}"
[ -n "${DENY_CODE}" ] && echo "  JSON-RPC 错误码：${DENY_CODE}"

ALLOW_DENIED=0; DENY_DENIED=0
[ "${ALLOW_STATUS}" = "403" ] && ALLOW_DENIED=1
[ "${DENY_STATUS}" = "403" ] && DENY_DENIED=1

if [ "${DENY_DENIED}" = "1" ] && [ "${ALLOW_DENIED}" = "0" ]; then
  echo "  ✔ 这条路径上工具级强制力**在生效**（不允许的工具真的被拒了）"
  exit 0
fi
if [ "${DENY_DENIED}" = "0" ] && [ "${ALLOW_DENIED}" = "0" ]; then
  echo "  ✘ 这条路径上工具级强制力**没生效**：不在允许集的工具被放行了" >&2
  echo "    ★ 后果：这把 key 能调**任何**工具，而监控上看不出异常。" >&2
  exit 1
fi

# ★ 两个都被拒 ⇒ 分不清「执法在生效」与「这把 key 全被禁 / 这条路径不认这把 key」。
fail_unverified "both_denied" "allow=${ALLOW_STATUS} deny=${DENY_STATUS}"
