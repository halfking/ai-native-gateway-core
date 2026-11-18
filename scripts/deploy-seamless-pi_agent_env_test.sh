#!/usr/bin/env bash
# =============================================================================
# scripts/deploy-seamless-pi_agent_env_test.sh
#
# 判据门：PI 智能体开关（V2 pipeline / 错误采集 / LLM 分析）在**部署引擎**里
# 的跨部署保留。与 asm/autollm env 门同族同法。
#
# 为什么要有这道门（2026-11-18，245 开闸轮）：
#   网关侧全部 opt-in（cmd/gateway/pi_agent_init.go），而引擎的 env 文件
#   **每次部署都被重写** ⇒ 运维手工加进 gateway.env 的 PI 行下一次部署即
#   静默丢失 ⇒「开关开着」变成看不见的故障。引擎必须沿用上次 env 文件
#   里的值（部署机环境变量优先）。
#
# 测法：按哨兵注释抽取引擎里的真代码块在沙箱里跑 ⇒ 测的是真实现。
#
# 用法: bash scripts/deploy-seamless-pi_agent_env_test.sh
# 退出码: 0 = 全过；1 = 有断言红
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENGINE="$SCRIPT_DIR/deploy-seamless-docker.sh"

PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ok()   { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }
check() { # <描述> <期望> <实际>
  if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1（期望 [$2] 实际 [$3]）"; fi
}

extract_block() {
  awk -v b="$2" -v e="$3" '
    index($0, b) { f = 1 }
    f { print }
    index($0, e) { f = 0 }
  ' "$1"
}

echo "=== 判据门：deploy-seamless-pi_agent_env_test ==="
[[ -f "$ENGINE" ]] || { echo "✗ 找不到引擎：$ENGINE"; exit 1; }

# ── 场景①：哨兵成对 + 函数存在 ──────────────────────────────────────────
echo "[1] 结构：哨兵成对、函数存在"
PI_BLK="$(extract_block "$ENGINE" '# ── PI-AGENT-ENV-BLOCK:BEGIN ──' '# ── PI-AGENT-ENV-BLOCK:END ──')"
if [[ -n "$PI_BLK" && "$PI_BLK" == *"dd_pi_agent_env()"* ]]; then
  ok "PI-AGENT-ENV-BLOCK 哨兵成对且含 dd_pi_agent_env()"
else
  bad "PI-AGENT-ENV-BLOCK 缺失或不含 dd_pi_agent_env()"
fi
INJ_BLK="$(extract_block "$ENGINE" '# ── PI-AGENT-ENV-INJECT:BEGIN ──' '# ── PI-AGENT-ENV-INJECT:END ──')"
if [[ -n "$INJ_BLK" && "$INJ_BLK" == *"LLMGatewayPIErrorAnalysisEnabled"* ]]; then
  ok "PI-AGENT-ENV-INJECT 哨兵成对且注入分析开关"
else
  bad "PI-AGENT-ENV-INJECT 缺失"
fi

# _dd_unq 依赖（与 asm 门同款：被测函数体内调用它）
_DD_UNQ_FN="$(sed -n '/^_dd_unq() {/,/^}/p' "$ENGINE")"
[[ -n "$_DD_UNQ_FN" ]] || { echo "✗ 引擎里提取不到 _dd_unq 函数"; exit 1; }

# ── 场景②：env 文件上次值被沿用（跨部署保留的核心判据）────────────────
echo "[2] 沿用：env 文件里的上次值在部署机未 export 时生效"
ENVF="$TMP/gateway.env"
cat > "$ENVF" <<'EOF'
export LLM_GATEWAY_USE_V2_PIPELINE=true
export LLMGatewayPIErrorCollectorEnabled=true
export LLMGatewayPIErrorAnalysisEnabled=true
export LLMGatewayPIErrorAnalysisModel=glm-5.3-flash
export LLMGatewayPIErrorAnalysisTimeoutSec=180
export LLM_GATEWAY_ANALYSIS_BASE_URL=https://llm.kxpms.cn/v1
export LLM_GATEWAY_ANALYSIS_API_KEY='sk-pi-test-value'
EOF
(
  eval "$_DD_UNQ_FN"
  eval "$(extract_block "$ENGINE" '# ── PI-AGENT-ENV-BLOCK:BEGIN ──' '# ── PI-AGENT-ENV-BLOCK:END ──' | sed -n '/^dd_pi_agent_env()/,/^}/p')"
  unset LLM_GATEWAY_USE_V2_PIPELINE LLMGatewayPIErrorCollectorEnabled \
        LLMGatewayPIErrorAnalysisEnabled LLMGatewayPIErrorAnalysisModel \
        LLMGatewayPIErrorAnalysisTimeoutSec LLM_GATEWAY_ANALYSIS_BASE_URL \
        LLM_GATEWAY_ANALYSIS_API_KEY
  dd_pi_agent_env "$ENVF"
  printf 'V2=%s|COLL=%s|ANA=%s|MODEL=%s|TMO=%s|URL=%s|KEY=%s\n' \
    "${LLM_GATEWAY_USE_V2_PIPELINE:-}" "${LLMGatewayPIErrorCollectorEnabled:-}" \
    "${LLMGatewayPIErrorAnalysisEnabled:-}" "${LLMGatewayPIErrorAnalysisModel:-}" \
    "${LLMGatewayPIErrorAnalysisTimeoutSec:-}" "${LLM_GATEWAY_ANALYSIS_BASE_URL:-}" \
    "${LLM_GATEWAY_ANALYSIS_API_KEY:-}"
) > "$TMP/out2" 2>&1
OUT="$(tail -1 "$TMP/out2")"
check "七键全部从 env 文件沿用（含引号剥离）" \
  "V2=true|COLL=true|ANA=true|MODEL=glm-5.3-flash|TMO=180|URL=https://llm.kxpms.cn/v1|KEY=sk-pi-test-value" "$OUT"

# ── 场景③：部署机环境变量优先于 env 文件 ────────────────────────────────
echo "[3] 优先级：部署机 export 覆盖 env 文件上次值"
(
  eval "$_DD_UNQ_FN"
  eval "$(extract_block "$ENGINE" '# ── PI-AGENT-ENV-BLOCK:BEGIN ──' '# ── PI-AGENT-ENV-BLOCK:END ──' | sed -n '/^dd_pi_agent_env()/,/^}/p')"
  LLM_GATEWAY_USE_V2_PIPELINE=false
  LLMGatewayPIErrorAnalysisModel=deepseek-v4-flash
  dd_pi_agent_env "$ENVF"
  printf '%s/%s' "$LLM_GATEWAY_USE_V2_PIPELINE" "$LLMGatewayPIErrorAnalysisModel"
) > "$TMP/out3" 2>&1
check "部署机值不被 env 文件覆盖" "false/deepseek-v4-flash" "$(tail -1 "$TMP/out3")"

# ── 场景④：%q 累积解码（防 E2BIG 同族回归）──────────────────────────────
echo "[4] 解码：双引号 %q 形态被剥净（防逐层累积）"
ENVF2="$TMP/gateway2.env"
printf 'export LLM_GATEWAY_ANALYSIS_BASE_URL=%q\n' "https://llm.kxpms.cn/v1" > "$ENVF2"
(
  eval "$_DD_UNQ_FN"
  eval "$(extract_block "$ENGINE" '# ── PI-AGENT-ENV-BLOCK:BEGIN ──' '# ── PI-AGENT-ENV-BLOCK:END ──' | sed -n '/^dd_pi_agent_env()/,/^}/p')"
  unset LLM_GATEWAY_ANALYSIS_BASE_URL
  dd_pi_agent_env "$ENVF2"
  printf '%s' "$LLM_GATEWAY_ANALYSIS_BASE_URL"
) > "$TMP/out4" 2>&1
check "URL 无残余引号/转义" "https://llm.kxpms.cn/v1" "$(tail -1 "$TMP/out4")"

echo
if [[ $FAIL -eq 0 ]]; then
  echo "✅ 全部通过（${PASS}）"; exit 0
else
  echo "❌ ${FAIL} 项失败（${PASS} 过）"; exit 1
fi
