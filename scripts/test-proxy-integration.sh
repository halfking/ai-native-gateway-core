#!/usr/bin/env bash
# 代理管理系统端到端集成测试。
#
# 覆盖链路：添加订阅 → 刷新解析节点 → 登记本地网桥 → 健康检查 → 汇总状态
#          → 经代理探活海外供应商（Groq）→ 清理。
#
# 关键背景（务必理解，否则会误判结果）：
#   真实订阅（NPS/机场）返回 Clash/Mihomo YAML，节点几乎都是 trojan/vless。
#   Go 的 net/http 只能通过 http/https/socks5 代理拨号，因此这些节点导入后
#   dialable_count = 0，本身无法直接使用。必须先用本地 mihomo/xray 暴露一个
#   http 或 socks5 入口（如 Clash 的 mixed-port 127.0.0.1:7897），
#   再把该入口登记为节点。本脚本会显式验证这一点。
#
# 用法：
#   BASE_URL=http://127.0.0.1:8781 \
#   LLM_GATEWAY_ADMIN_USER=admin LLM_GATEWAY_ADMIN_PASSWORD=... \
#   scripts/test-proxy-integration.sh
#
# 可选环境变量：
#   PROXY_SUBSCRIBE_URL  订阅地址（默认 252 NPS）
#   BRIDGE_HOST/BRIDGE_PORT/BRIDGE_PROTOCOL  本地网桥入口（默认 127.0.0.1:7897 http）
#   PROBE_BASE_URL       用于验证代理出口的海外供应商（默认 Groq）
#   KEEP_DATA=1          保留测试数据，便于人工排查

set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8781}"
PROXY_SUBSCRIBE_URL="${PROXY_SUBSCRIBE_URL:-http://115.29.212.252:8080/subscribe?token=4956b9532968e00e9f0e710e4ccf262d}"
BRIDGE_PROTOCOL="${BRIDGE_PROTOCOL:-http}"
BRIDGE_HOST="${BRIDGE_HOST:-127.0.0.1}"
BRIDGE_PORT="${BRIDGE_PORT:-7897}"
PROBE_BASE_URL="${PROBE_BASE_URL:-https://api.groq.com/openai/v1}"
SUB_NAME="${SUB_NAME:-proxy-e2e-$$}"

# 下面的 python3 内联脚本通过 os.environ 读取这些值，必须导出。
export PROXY_SUBSCRIBE_URL BRIDGE_PROTOCOL BRIDGE_HOST BRIDGE_PORT PROBE_BASE_URL SUB_NAME

PASS=0
FAIL=0
SUB_ID=""
BRIDGE_ID=""
TOKEN=""

c_green() { printf '\033[32m%s\033[0m\n' "$1"; }
c_red()   { printf '\033[31m%s\033[0m\n' "$1"; }
c_dim()   { printf '\033[2m%s\033[0m\n' "$1"; }

ok()   { PASS=$((PASS + 1)); c_green "  PASS  $1"; }
bad()  { FAIL=$((FAIL + 1)); c_red   "  FAIL  $1"; }
info() { c_dim "        $1"; }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }
die()  { c_red "FATAL: $1"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }
need curl
need python3

# jq 未必存在，用 python3 做 JSON 取值，避免额外依赖。
jget() { # jget <json> <python-expr over variable d>
  python3 -c '
import json,sys
try:
    d=json.loads(sys.argv[1])
except Exception:
    print(""); sys.exit(0)
try:
    v=eval(sys.argv[2],{},{"d":d})
except Exception:
    print(""); sys.exit(0)
print("" if v is None else v)
' "$1" "$2"
}

# api 会在命令替换的子 shell 中执行，因此状态码不能靠变量回传（赋值不会传出子 shell），
# 必须写入文件，由父 shell 通过 code() 读取。
CODE_FILE="$(mktemp)"

api() { # api <method> <path> [json-body] -> body 走 stdout，状态码写入 CODE_FILE
  local method="$1" path="$2" body="${3:-}"
  local tmp; tmp="$(mktemp)"
  local code
  if [ -n "$body" ]; then
    code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
      -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
      --data "$body" "$BASE_URL$path" 2>/dev/null)" || code="000"
  else
    code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
      -H "Authorization: Bearer $TOKEN" "$BASE_URL$path" 2>/dev/null)" || code="000"
  fi
  printf '%s' "$code" > "$CODE_FILE"
  cat "$tmp"
  rm -f "$tmp"
}

code() { cat "$CODE_FILE" 2>/dev/null || printf '000'; }

cleanup() {
  rm -f "$CODE_FILE" 2>/dev/null || true
  if [ "${KEEP_DATA:-0}" = "1" ]; then
    info "KEEP_DATA=1 — 保留订阅 id=$SUB_ID"
    return
  fi
  if [ -n "$SUB_ID" ]; then
    api DELETE "/api/proxy/subscriptions/$SUB_ID" >/dev/null 2>&1 || true
    info "已清理订阅 id=${SUB_ID}（节点由外键级联删除）"
  fi
}
trap cleanup EXIT

# ── 0. 登录 ────────────────────────────────────────────────────────────────
step "0. 前置检查与登录"
[ -n "${LLM_GATEWAY_ADMIN_USER:-}" ]     || die "需要 LLM_GATEWAY_ADMIN_USER"
[ -n "${LLM_GATEWAY_ADMIN_PASSWORD:-}" ] || die "需要 LLM_GATEWAY_ADMIN_PASSWORD"

curl -fsS --max-time 5 "$BASE_URL/healthz" >/dev/null 2>&1 \
  || curl -fsS --max-time 5 "$BASE_URL/api/system/version" >/dev/null 2>&1 \
  || die "网关不可达：$BASE_URL"
ok "网关可达 $BASE_URL"

LOGIN_BODY="$(python3 -c '
import json,os
print(json.dumps({"username":os.environ["LLM_GATEWAY_ADMIN_USER"],
                  "password":os.environ["LLM_GATEWAY_ADMIN_PASSWORD"]}))')"
LOGIN_RESP="$(curl -sS -H 'Content-Type: application/json' -X POST \
  "$BASE_URL/api/auth/token" --data "$LOGIN_BODY")"
TOKEN="$(jget "$LOGIN_RESP" 'd.get("access_token","")')"
[ -n "$TOKEN" ] || die "登录失败：$LOGIN_RESP"
ok "已获取管理员 token"

# 本地网桥是否在监听（不在也继续，用于验证失败路径的报错质量）
BRIDGE_UP=0
if curl -sS --max-time 15 -x "$BRIDGE_PROTOCOL://$BRIDGE_HOST:$BRIDGE_PORT" \
     -o /dev/null "$PROBE_BASE_URL/models" 2>/dev/null; then
  BRIDGE_UP=1
  ok "本地网桥可用 $BRIDGE_PROTOCOL://$BRIDGE_HOST:$BRIDGE_PORT"
else
  info "本地网桥 $BRIDGE_HOST:$BRIDGE_PORT 不可用 —— 相关断言将降级为「报错是否清晰」"
fi

# ── 1. 直连基线 ────────────────────────────────────────────────────────────
step "1. 直连基线（确认目标确实被墙）"
DIRECT_CODE="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 "$PROBE_BASE_URL/models" 2>/dev/null)" || DIRECT_CODE=""
[ -n "$DIRECT_CODE" ] && [ "$DIRECT_CODE" != "000" ] || DIRECT_CODE="000"
info "直连 $PROBE_BASE_URL/models → HTTP $DIRECT_CODE"
if [ "$DIRECT_CODE" = "403" ] || [ "$DIRECT_CODE" = "000" ]; then
  ok "直连被阻断（HTTP ${DIRECT_CODE}）——正是代理要解决的问题"
else
  info "直连返回 ${DIRECT_CODE}（当前网络可能无需代理，代理链路仍会被验证）"
fi

# ── 2. 创建订阅 ────────────────────────────────────────────────────────────
step "2. 创建订阅"
CREATE_BODY="$(python3 -c '
import json,os
print(json.dumps({"name":os.environ["SUB_NAME"],
                  "subscribe_url":os.environ["PROXY_SUBSCRIBE_URL"],
                  "notes":"created by test-proxy-integration.sh"}))')"
RESP="$(api POST /api/proxy/subscriptions "$CREATE_BODY")"
if [ "$(code)" = "201" ]; then
  SUB_ID="$(jget "$RESP" 'd.get("id","")')"
  export SUB_ID
  ok "订阅已创建 id=$SUB_ID"
else
  bad "创建订阅失败 HTTP $(code): $RESP"
  die "无法继续"
fi

# 校验非法输入被拒
RESP="$(api POST /api/proxy/subscriptions '{"name":"x","subscribe_url":"ftp://bad"}')"
if [ "$(code)" = "400" ]; then ok "非 http/https 订阅地址被拒（400）"; else bad "非法订阅地址未被拒，HTTP $(code)"; fi

# ── 3. 刷新订阅（解析真实 Clash YAML）────────────────────────────────────
step "3. 刷新订阅并解析节点"
RESP="$(api POST "/api/proxy/subscriptions/$SUB_ID/refresh")"
if [ "$(code)" != "200" ]; then
  bad "刷新失败 HTTP $(code): $RESP"
else
  NODE_COUNT="$(jget "$RESP" 'd.get("node_count",0)')"
  DIALABLE="$(jget "$RESP" 'd.get("dialable_count",0)')"
  BY_PROTO="$(jget "$RESP" 'json.dumps(d.get("by_protocol",{}),ensure_ascii=False)')"
  WARNING="$(jget "$RESP" 'd.get("warning","")')"
  info "node_count=$NODE_COUNT dialable_count=$DIALABLE by_protocol=$BY_PROTO"

  if [ "${NODE_COUNT:-0}" -gt 0 ]; then
    ok "订阅解析出 $NODE_COUNT 个节点（Clash YAML 格式）"
  else
    bad "订阅未解析出任何节点"
  fi

  # 核心断言：trojan/vless 不能被当作可用代理
  if [ "${NODE_COUNT:-0}" -gt 0 ] && [ "${DIALABLE:-0}" = "0" ]; then
    if [ -n "$WARNING" ]; then
      ok "0 个可拨号节点时给出了可操作告警（未把 trojan/vless 误报为可用）"
      info "warning: $WARNING"
    else
      bad "有节点但 dialable=0，却没有给出告警——运维会误以为代理已可用"
    fi
  elif [ "${DIALABLE:-0}" -gt 0 ]; then
    ok "订阅中存在 $DIALABLE 个可拨号节点（http/https/socks5）"
  fi
fi

# ── 4. 节点列表与 dialable 过滤 ──────────────────────────────────────────
step "4. 节点列表 / dialable 过滤"
RESP="$(api GET "/api/proxy/nodes?subscription_id=$SUB_ID&dialable=false")"
if [ "$(code)" = "200" ]; then
  N="$(jget "$RESP" 'd.get("total",0)')"
  ok "dialable=false 过滤可用，返回 $N 个不可拨号节点"
else
  bad "节点列表失败 HTTP $(code)"
fi
RESP="$(api GET "/api/proxy/nodes?subscription_id=$SUB_ID")"
if [ "$(code)" = "200" ]; then
  if printf '%s' "$RESP" | grep -q '"password"'; then
    bad "节点响应中出现了 password 字段——不得泄露代理凭据"
  else
    ok "节点响应未泄露 password（仅 has_password 布尔）"
  fi
fi

# ── 5. 登记本地网桥节点 ──────────────────────────────────────────────────
step "5. 登记本地 mihomo/xray 网桥入口"
# 先确认非法协议被拒（trojan 不能手工登记为可用节点）
RESP="$(api POST /api/proxy/nodes \
  "{\"subscription_id\":$SUB_ID,\"name\":\"bad\",\"protocol\":\"trojan\",\"server\":\"x\",\"port\":443}")"
if [ "$(code)" = "400" ]; then ok "手工登记 trojan 节点被拒（400）"; else bad "trojan 节点竟被接受，HTTP $(code)"; fi

BRIDGE_BODY="$(python3 -c '
import json,os
print(json.dumps({"subscription_id":int(os.environ["SUB_ID"]),
                  "name":"local-bridge",
                  "protocol":os.environ["BRIDGE_PROTOCOL"],
                  "server":os.environ["BRIDGE_HOST"],
                  "port":int(os.environ["BRIDGE_PORT"]),
	                  "health_check_url":"https://www.google.com/generate_204"}))')"
RESP="$(api POST /api/proxy/nodes "$BRIDGE_BODY")"
if [ "$(code)" = "201" ]; then
  BRIDGE_ID="$(jget "$RESP" 'd.get("id","")')"
  IS_DIALABLE="$(jget "$RESP" 'd.get("dialable","")')"
  ok "网桥节点已登记 id=$BRIDGE_ID dialable=$IS_DIALABLE"
  [ "$IS_DIALABLE" = "True" ] || bad "网桥节点应为 dialable=true"
else
  bad "登记网桥节点失败 HTTP $(code): $RESP"
fi

# ── 6. 健康检查 ────────────────────────────────────────────────────────────
step "6. 网桥健康检查"
if [ -n "$BRIDGE_ID" ]; then
  RESP="$(api POST "/api/proxy/nodes/$BRIDGE_ID/health-check")"
  HC_OK="$(jget "$RESP" 'd.get("ok","")')"
  HC_MS="$(jget "$RESP" 'd.get("response_time_ms","")')"
  HC_ERR="$(jget "$RESP" 'd.get("error","")')"
  info "health-check ok=$HC_OK response_time_ms=$HC_MS ${HC_ERR:+error=$HC_ERR}"
  if [ "$BRIDGE_UP" = "1" ]; then
    if [ "$HC_OK" = "True" ]; then ok "网桥健康检查通过（${HC_MS}ms）"; else bad "网桥在监听但健康检查失败：$HC_ERR"; fi
  else
    if [ -n "$HC_ERR" ]; then ok "网桥不可用时健康检查如实报错"; else bad "网桥不可用却报告成功"; fi
  fi
fi

# ── 7. 汇总状态与节点选择 ────────────────────────────────────────────────
step "7. 汇总状态 / 最优节点选择"
RESP="$(api GET /api/proxy/status)"
if [ "$(code)" = "200" ]; then
  SEL_NAME="$(jget "$RESP" '(d.get("selected_node") or {}).get("name","")')"
  SEL_ERR="$(jget "$RESP" 'd.get("selection_error","")')"
  TOTAL_DIALABLE="$(jget "$RESP" 'd.get("dialable_count",0)')"
  info "dialable_count=$TOTAL_DIALABLE selected=${SEL_NAME:-none} ${SEL_ERR:+selection_error=$SEL_ERR}"
  if [ -n "$SEL_NAME" ]; then
    ok "选出了可拨号节点：$SEL_NAME"
  else
    if printf '%s' "$SEL_ERR" | grep -qi "mihomo\|dialable"; then
      ok "无可拨号节点时给出可操作的选择错误"
    else
      bad "既没选出节点，错误信息也不可操作：$SEL_ERR"
    fi
  fi
else
  bad "/api/proxy/status 失败 HTTP $(code)"
fi

# ── 8. 经代理探活海外供应商 ──────────────────────────────────────────────
step "8. free-pool 经代理探活（use_proxy）"
PROBE_BODY="$(python3 -c '
import json,os
print(json.dumps({"base_url":os.environ["PROBE_BASE_URL"],
                  "api_key":"dummy-key-for-reachability-test",
                  "catalog_code":"proxy-e2e-probe",
                  "probe_first":True,
                  "use_proxy":True,
                  "force_skip_probe":False}))')"
RESP="$(api POST /api/free-pool/quick-entry "$PROBE_BODY")"
PROBE_EGRESS="$(jget "$RESP" '(d.get("probe") or {}).get("egress","")')"
PROBE_STATUS="$(jget "$RESP" '(d.get("probe") or {}).get("status_code","")')"
PROBE_NODE="$(jget "$RESP" 'd.get("proxy_node","")')"
PROBE_ERRDETAIL="$(jget "$RESP" '(d.get("error") or {}).get("detail","") if isinstance(d.get("error"),dict) else d.get("error","")')"
info "HTTP $(code) egress=$PROBE_EGRESS status_code=$PROBE_STATUS node=$PROBE_NODE"

if [ "$BRIDGE_UP" = "1" ]; then
  if [ "$PROBE_EGRESS" = "proxy" ]; then
    ok "探活确实经由代理出口（egress=proxy，节点=${PROBE_NODE}）"
  else
    bad "use_proxy=true 但 egress=$PROBE_EGRESS"
  fi
  # 401/200 表示已到达上游；403 表示仍被墙
  case "$PROBE_STATUS" in
    401|200|400|422) ok "已到达上游供应商（HTTP ${PROBE_STATUS}），代理链路打通" ;;
    403) bad "仍返回 403 —— 代理未生效，请求可能仍走直连" ;;
    *)   info "上游返回 ${PROBE_STATUS}，人工确认" ;;
  esac
else
  if printf '%s' "$RESP$PROBE_ERRDETAIL" | grep -qi "代理\|proxy\|dialable"; then
    ok "无可用代理时明确拒绝，且未静默回退直连"
  else
    bad "无可用代理时的错误信息不清晰：$RESP"
  fi
fi

# use_proxy=false 必须保持原有直连行为
step "9. use_proxy=false 回归（默认行为不变）"
PROBE_BODY2="$(python3 -c '
import json,os
print(json.dumps({"base_url":os.environ["PROBE_BASE_URL"],
                  "api_key":"dummy","catalog_code":"proxy-e2e-direct",
                  "probe_first":True,"use_proxy":False}))')"
RESP="$(api POST /api/free-pool/quick-entry "$PROBE_BODY2")"
EG2="$(jget "$RESP" '(d.get("probe") or {}).get("egress","")')"
if [ "$EG2" = "direct" ]; then ok "use_proxy=false 时 egress=direct（行为未变）"; else bad "默认路径 egress=${EG2}，应为 direct"; fi

# ── 汇总 ──────────────────────────────────────────────────────────────────
step "结果"
printf '  通过: %d\n  失败: %d\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  c_red "集成测试失败"
  exit 1
fi
c_green "集成测试全部通过"
