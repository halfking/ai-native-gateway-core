#!/bin/bash
# =====================================================================
# hostedtask §6.0 跨仓库门禁 · 配置自检与缺失项报告（R63）
#
# 用途：在网关宿主机上自检任务托管（hosted-task-delegation-design §6.0）
# 的三项配置门禁。任一项缺失，reconciler 启动日志会出现 ACC not
# configured 告警，dispatch 轮次将以 dispatch_degraded 事件降级
# （bg/hosted_task_reconciler.go run() / dispatchPass()）。
#
#   门禁1  companion 常驻网关宿主机（systemd/进程探测，尽力而为）
#   门禁3a ACC service JWT —— 必须可解码且含 tenant_id claim（§6.0 门禁3）
#   门禁3b Memora service JWT（v2 契约）—— 网关 env 仅持有 BASE_URL/
#          API_KEY 写回配置；v2 契约 JWT 属 companion 侧职责（跨仓库），
#          缺失按 SKIPPED-CONFIG 记录（设计 §8 验收口径），人工核对
#
# 用法：
#   scripts/hostedtask-config-gate-check.sh                 # 读当前环境
#   scripts/hostedtask-config-gate-check.sh --env-file /path/to/.env
#
# 退出码：0=无缺失（或特性未启用）；1=存在缺失项（将 dispatch_degraded）
#
# 安全：脚本只输出 JWT 的 claim 结构与 SHA256 指纹前 12 位，绝不回显
# token 本体。
# =====================================================================
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

MISSING=0
WARNINGS=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      [[ -f "${2:-}" ]] || { echo "env file not found: ${2:-}" >&2; exit 2; }
      set -a; # shellcheck disable=SC1090
      . "$2"; set +a
      shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

ok()   { echo -e "${GREEN}✓${NC} $1"; }
miss() { echo -e "${RED}✗ MISSING${NC} $1"; MISSING=$((MISSING+1)); }
warn() { echo -e "${YELLOW}⚠${NC} $1"; WARNINGS=$((WARNINGS+1)); }
info() { echo -e "${BLUE}·${NC} $1"; }

# JWT payload（第 2 段）base64url 解码；非 3 段结构输出空串。
jwt_payload() {
  local seg
  seg="$(printf '%s' "$1" | cut -d. -f2)"
  [[ "$(printf '%s' "$1" | awk -F. '{print NF}')" -eq 3 ]] || { printf ''; return; }
  seg="$(printf '%s' "$seg" | tr '_-' '/+')"
  case $(( ${#seg} % 4 )) in
    2) seg="${seg}==" ;;
    3) seg="${seg}="  ;;
  esac
  printf '%s' "$seg" | openssl base64 -d -A 2>/dev/null || printf ''
}

fp() { printf '%s' "$1" | openssl sha256 2>/dev/null | awk '{print $NF}' | cut -c1-12; }

echo -e "${BLUE}== hostedtask §6.0 配置门禁自检 ==${NC}"

# ── 0. 特性总开关 ────────────────────────────────────────────────────
if [[ "${LLM_GATEWAY_HOSTED_TASKS_ENABLED:-}" != "true" ]]; then
  info "LLM_GATEWAY_HOSTED_TASKS_ENABLED != true：reconciler 与端点未装配，门禁不适用"
  exit 0
fi
ok "LLM_GATEWAY_HOSTED_TASKS_ENABLED=true"

# ── 门禁1：companion 常驻本机（尽力而为探测）─────────────────────────
if pgrep -f agent-companion >/dev/null 2>&1 \
   || systemctl list-units --type=service 2>/dev/null | grep -qi agent-companion; then
  ok "门禁1 companion：本机检测到 agent-companion（进程/systemd）"
else
  warn "门禁1 companion：本机未检测到 agent-companion 进程/systemd 单元（跨仓库部署项；若 companion 部署在其他宿主机请人工确认 ACC runtime 注册）"
fi

# ── 门禁3a：ACC service JWT ──────────────────────────────────────────
ACC_URL="${LLM_GATEWAY_ACC_BASE_URL:-}"
if [[ -n "$ACC_URL" ]]; then
  ok "LLM_GATEWAY_ACC_BASE_URL=$ACC_URL"
  if command -v curl >/dev/null 2>&1; then
    if curl -sS -o /dev/null -m 3 "$ACC_URL" 2>/dev/null \
       || curl -sS -o /dev/null -m 3 "$ACC_URL/healthz" 2>/dev/null; then
      ok "ACC base URL 可达（TCP/HTTP 探测）"
    else
      warn "ACC base URL 探测不可达（网络项非配置项；dispatch 时将表现为 dispatch_degraded 重试）"
    fi
  fi
else
  miss "LLM_GATEWAY_ACC_BASE_URL（Runtime Control 地址）"
fi

ACC_TOKEN="${LLM_GATEWAY_ACC_SERVICE_TOKEN:-}"
if [[ -z "$ACC_TOKEN" ]]; then
  miss "LLM_GATEWAY_ACC_SERVICE_TOKEN（ACC service JWT）→ dispatch 全部降级 dispatch_degraded"
else
  payload="$(jwt_payload "$ACC_TOKEN")"
  if [[ -z "$payload" ]]; then
    miss "ACC service JWT 结构非法（非 header.payload.signature 三段）→ dispatch 全部降级"
  else
    ok "ACC service JWT 结构合法（token sha256:${ACC_TOKEN:+$(fp "$ACC_TOKEN")}）"
    if printf '%s' "$payload" | grep -q '"tenant_id"'; then
      tid="$(printf '%s' "$payload" | grep -o '"tenant_id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*:[[:space:]]*"\(.*\)"/\1/')"
      ok "ACC service JWT 含 tenant_id claim（§6.0 门禁3）：tenant_id=${tid:-<empty>}"
      [[ -n "$tid" ]] || warn "tenant_id claim 存在但为空值"
    else
      miss "ACC service JWT 缺 tenant_id claim（§6.0 门禁3：service JWT 必须携带租户声明）"
    fi
    exp="$(printf '%s' "$payload" | grep -o '"exp"[[:space:]]*:[[:space:]]*[0-9]*' | grep -o '[0-9]*$' | head -1)"
    if [[ -n "$exp" ]]; then
      now="$(date +%s)"
      # 用 [[ -le ]] 而非 (( ))：bash 5.3 在含 CJK 字面量的解析单元里对
      # set -u + 算术变量名有误报 unbound 的解析异常（R63 实测）。
      if [[ "$exp" -le "$now" ]]; then
        # 全角字符紧邻 $var 时必须用 ${var}：bash 5.3 参数展开会把紧随的
        # 多字节首字节吞进变量名（$exp）→ 查找 exp\xef 报 unbound，R63 实测）。
        miss "ACC service JWT 已过期（exp=$exp < now=${now}）→ dispatch 全部降级"
      else
        ok "ACC service JWT 未过期（exp=${exp}）"
      fi
    else
      warn "ACC service JWT 无 exp claim（无法自检有效期）"
    fi
  fi
fi

if [[ -n "${LLM_GATEWAY_ACC_RUNTIME_ID:-}" ]]; then
  ok "LLM_GATEWAY_ACC_RUNTIME_ID=${LLM_GATEWAY_ACC_RUNTIME_ID}"
else
  miss "LLM_GATEWAY_ACC_RUNTIME_ID（companion 注册的 runtime；缺失则 dispatch 无法指定执行者）"
fi

# ── 门禁3b：Memora（v2 契约 JWT 为 companion 侧职责）────────────────
if [[ -n "${LLM_GATEWAY_MEMORA_BASE_URL:-}" && -n "${LLM_GATEWAY_MEMORA_API_KEY:-}" ]]; then
  ok "门禁3b Memora：网关侧写回配置在位（BASE_URL + API_KEY）"
else
  warn "门禁3b Memora：网关侧写回配置缺失（LLM_GATEWAY_MEMORA_BASE_URL / LLM_GATEWAY_MEMORA_API_KEY）"
fi
info "Memora service JWT（v2 契约）属 companion 侧持有【跨仓库】→ SKIPPED-CONFIG：请按设计 §6.0 门禁3 人工核对 companion 的 MEMORA service token"

# ── 受理面：workspace 白名单 ─────────────────────────────────────────
if [[ -n "${LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP:-}" ]]; then
  n="$(printf '%s' "${LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP}" | awk -F, '{print NF}')"
  ok "LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP 在位（${n} 个 workspace）"
else
  miss "LLM_GATEWAY_HOSTED_TASK_WORKSPACE_MAP（空白名单 ⇒ 所有委托 400 UNKNOWN_WORKSPACE）"
fi

# ── 汇总 ─────────────────────────────────────────────────────────────
echo -e "${BLUE}== 汇总 ==${NC}"
if (( MISSING > 0 )); then
  echo -e "${RED}缺失 ${MISSING} 项${NC}（警告 ${WARNINGS} 项）：reconciler 启动日志将出现 ACC not configured 告警，"
  echo "dispatch 轮次以 dispatch_degraded 事件降级并停留 dispatching（bg/hosted_task_reconciler.go run()/dispatchPass()），"
  echo "补齐配置并重启网关即恢复。"
  exit 1
fi
echo -e "${GREEN}无缺失项（警告 ${WARNINGS} 项）。dispatch_degraded 降级路径不应触发。${NC}"
exit 0
