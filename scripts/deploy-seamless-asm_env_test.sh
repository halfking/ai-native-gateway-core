#!/usr/bin/env bash
# =============================================================================
# scripts/deploy-seamless-asm_env_test.sh
#
# 判据门：网关 → ASM 事件投递开关在**部署引擎**里的落地。
#
# 为什么要有这道门（2026-10-21）：
#   网关侧是双开关 opt-in（cmd/gateway/main.go：ASM_INTERNAL_ENDPOINT +
#   AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET 都非空才起 outbox dispatcher）。
#   而引擎的 env 文件**每次部署都被重写** ⇒ 只在部署机 export 一次，下一次
#   部署就没了 ⇒ 「开关开了但下次部署静默失效」是一个**看不见**的故障。
#   ⇒ 引擎必须把这两个值**沿用**上次 env 文件里的（运维手工改过的那份）。
#
# 测法：按哨兵注释**抽取引擎里的真代码块**在沙箱里跑 ⇒ 测的是真实现，
#       不是把逻辑复制一份的假实现（复制品会跟引擎一起漂移，测了等于没测）。
#
# 用法: bash scripts/deploy-seamless-asm_env_test.sh
# 退出码: 0 = 全过；1 = 有断言红
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENGINE="$SCRIPT_DIR/deploy-seamless-docker.sh"
WRAPPER="$SCRIPT_DIR/deploy-211.sh"

PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ok()   { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }
check() { # <描述> <期望> <实际>
  if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1（期望 [$2] 实际 [$3]）"; fi
}

# 按哨兵抽块：BEGIN/END 两行注释之间的全部内容（含注释行本身，eval 无害）
extract_block() {
  awk -v b="$2" -v e="$3" '
    index($0, b) { f = 1 }
    f { print }
    index($0, e) { f = 0 }
  ' "$1"
}

echo "=== 判据门：deploy-seamless-asm_env_test ==="

[[ -f "$ENGINE" ]] || { echo "✗ 找不到引擎：$ENGINE"; exit 1; }
[[ -f "$WRAPPER" ]] || { echo "✗ 找不到外壳：$WRAPPER"; exit 1; }

# ── 场景①：哨兵成对 + 函数存在 ──────────────────────────────────────────
echo "[1] 结构：哨兵成对、函数存在"
ASM_BLK="$(extract_block "$ENGINE" '# ── ASM-ENV-BLOCK:BEGIN ──' '# ── ASM-ENV-BLOCK:END ──')"
INJ_BLK="$(extract_block "$ENGINE" '# ── ASM-ENV-INJECT:BEGIN ──' '# ── ASM-ENV-INJECT:END ──')"
if [[ -n "$ASM_BLK" && "$ASM_BLK" == *"dd_asm_event_env()"* ]]; then
  ok "ASM-ENV-BLOCK 哨兵成对且含 dd_asm_event_env()"
else
  bad "ASM-ENV-BLOCK 哨兵缺失或未含 dd_asm_event_env()"
fi
if [[ -n "$INJ_BLK" && "$INJ_BLK" == *"ASM_INTERNAL_ENDPOINT"* && "$INJ_BLK" == *"AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET"* ]]; then
  ok "ASM-ENV-INJECT 哨兵成对且含两个开关"
else
  bad "ASM-ENV-INJECT 哨兵缺失或未含两个开关"
fi

# ── 沙箱里跑真代码块 ─────────────────────────────────────────────────────
run_asm_env() { # <env_file> <preset_endpoint> <preset_secret> ⇒ 输出两行
  (
    set -uo pipefail
    ENV_FILE="$1"
    ASM_INTERNAL_ENDPOINT="$2"
    AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET="$3"
    log() { :; }
    eval "$ASM_BLK"
    dd_asm_event_env "$ENV_FILE"
    printf '%s\n' "$ASM_INTERNAL_ENDPOINT"
    printf '%s\n' "$ASM_EVENT_SECRET"
  )
}

# ── 场景②：沿用上次 env 文件里的值（跨部署保留）──────────────────────────
echo "[2] 沿用上次 env 文件（运维改过的那份）"
cat > "$TMP/env.keep" <<'EOF'
# llm-gateway-go @ 211（由 deploy-seamless-docker.sh 生成，勿提交）
export LLM_GATEWAY_STORAGE_MODE=full
export ASM_INTERNAL_ENDPOINT=http://ai-session-manager-asm-1:8782/internal/v1/events
export AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=keep-this-secret
EOF
OUT="$(run_asm_env "$TMP/env.keep" "" "")"
check "endpoint 从旧 env 文件读出" "http://ai-session-manager-asm-1:8782/internal/v1/events" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "secret 从旧 env 文件读出"   "keep-this-secret" "$(printf '%s\n' "$OUT" | sed -n 2p)"

# ── 场景③：部署机环境变量优先于 env 文件里的旧值 ─────────────────────────
echo "[3] 部署机环境变量优先"
OUT="$(run_asm_env "$TMP/env.keep" "http://from-deployer:9999/x" "deployer-secret")"
check "endpoint 取部署机值" "http://from-deployer:9999/x" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "secret 取部署机值"   "deployer-secret" "$(printf '%s\n' "$OUT" | sed -n 2p)"

# ── 场景④：bash %q 的引号形态要被剥掉 ────────────────────────────────────
echo "[4] shell 引号形态（bash %q）剥离"
cat > "$TMP/env.quoted" <<'EOF'
export ASM_INTERNAL_ENDPOINT='http://quoted:8782/internal/v1/events'
export AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET='quoted-secret'
EOF
OUT="$(run_asm_env "$TMP/env.quoted" "" "")"
check "endpoint 引号已剥" "http://quoted:8782/internal/v1/events" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "secret 引号已剥"   "quoted-secret" "$(printf '%s\n' "$OUT" | sed -n 2p)"

# ── 场景⑤：没有 env 文件 ⇒ 两个开关都空（不投事件）────────────────────────
echo "[5] 无 env 文件 ⇒ 开关为空"
OUT="$(run_asm_env "$TMP/does-not-exist" "" "")"
check "endpoint 空" "" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "secret 空"   "" "$(printf '%s\n' "$OUT" | sed -n 2p)"

# ── 场景⑤b：总结委托的两个变量同样走「部署机值 > env 文件 > 不设」 ────────
echo "[5b] 总结委托变量（LLM_GATEWAY_ASM_BASE_URL / _PLUGIN_ID）"
run_asm_delegate() { # <env_file> ⇒ 打印两个委托变量（与 dd_asm_event_env 同一函数）
  (
    set -uo pipefail
    ENV_FILE="$1"
    ASM_INTERNAL_ENDPOINT=""
    ASM_EVENT_SECRET=""
    LLM_GATEWAY_ASM_BASE_URL=""
    LLM_GATEWAY_ASM_PLUGIN_ID=""
    log() { :; }
    eval "$ASM_BLK"
    dd_asm_event_env "$ENV_FILE"
    printf '%s\n' "$LLM_GATEWAY_ASM_BASE_URL"
    printf '%s\n' "$LLM_GATEWAY_ASM_PLUGIN_ID"
  )
}
cat > "$TMP/env.asmdelegate" <<'EOF'
export LLM_GATEWAY_STORAGE_MODE=full
export LLM_GATEWAY_ASM_BASE_URL=http://ai-session-manager-asm-1:8782
EOF
OUT="$(run_asm_delegate "$TMP/env.asmdelegate")"
check "base_url 从旧 env 文件读出" "http://ai-session-manager-asm-1:8782" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "plugin_id 未设回落空（网关 main.go 兜底默认）" "" "$(printf '%s\n' "$OUT" | sed -n 2p)"
OUT="$(run_asm_delegate "$TMP/does-not-exist")"
check "无 env 文件 ⇒ base_url 空" "" "$(printf '%s\n' "$OUT" | sed -n 1p)"

# ── 场景⑥：注入 —— 双开关语义（只配一个 = 不注入）────────────────────────
echo "[6] 注入：两个都齐才注入"
run_inject() { # <endpoint> <secret> ⇒ 打印 GW_ENV（元素按行分隔）
  (
    set -uo pipefail
    ASM_INTERNAL_ENDPOINT="$1"
    ASM_EVENT_SECRET="$2"
    GW_ENV=()
    log() { :; }
    eval "$INJ_BLK"
    printf '%s\n' "${GW_ENV[@]+"${GW_ENV[@]}"}"
  )
}
# ★ GW_ENV 里每个 `-e KEY=VALUE` 是**两个独立数组元素**（-e 一个、KEY=VALUE 一个）
#   ⇒ 断言要按 argv 序列看（拼成一行再匹配），不能按「单行含 -e KEY=VALUE」匹配。
join_env() { printf '%s\n' "$1" | tr '\n' ' '; }
OUT="$(run_inject "http://asm:8782/internal/v1/events" "s3cret")"
JOIN="$(join_env "$OUT")"
check "endpoint 被注入" "1" "$(printf '%s' "$JOIN" | grep -c -- '-e ASM_INTERNAL_ENDPOINT=http://asm:8782/internal/v1/events')"
check "secret 被注入"   "1" "$(printf '%s' "$JOIN" | grep -c -- '-e AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=s3cret')"
check "argv 序列里 -e 出现两次（每个开关一个）" "2" "$(printf '%s\n' "$OUT" | grep -cx -- '-e')"

# ── 场景⑥b：总结委托变量注入（set -u 沙箱里未设也要容错）──────────────────
run_inject2() { # <base_url> <plugin_id> ⇒ 打印 GW_ENV
  (
    set -uo pipefail
    ASM_INTERNAL_ENDPOINT=""
    ASM_EVENT_SECRET=""
    LLM_GATEWAY_ASM_BASE_URL="$1"
    LLM_GATEWAY_ASM_PLUGIN_ID="$2"
    GW_ENV=()
    log() { :; }
    eval "$INJ_BLK"
    printf '%s\n' "${GW_ENV[@]+"${GW_ENV[@]}"}"
  )
}
OUT="$(run_inject2 "http://asm:8782" "ai-session-manager")"
JOIN="$(join_env "$OUT")"
check "总结委托 base_url 被注入" "1" "$(printf '%s' "$JOIN" | grep -c -- '-e LLM_GATEWAY_ASM_BASE_URL=http://asm:8782')"
check "总结委托 plugin_id 被注入" "1" "$(printf '%s' "$JOIN" | grep -c -- '-e LLM_GATEWAY_ASM_PLUGIN_ID=ai-session-manager')"
# 未设 base_url ⇒ 不注入（且 set -u 沙箱不炸 = 场景能跑到断言这一行）
OUT="$(
  (
    set -uo pipefail
    ASM_INTERNAL_ENDPOINT=""
    ASM_EVENT_SECRET=""
    GW_ENV=()
    log() { :; }
    eval "$INJ_BLK"
    printf '%s\n' "${GW_ENV[@]+"${GW_ENV[@]}"}"
  )
)"
check "未配置 ⇒ 总结委托不注入" "0" "$(printf '%s' "$OUT" | grep -c -- 'LLM_GATEWAY_ASM_BASE_URL')"

OUT="$(run_inject "http://asm:8782/internal/v1/events" "")"
JOIN="$(join_env "$OUT")"
check "只配 endpoint ⇒ 不注入任何开关（双开关语义）" "0" "$(printf '%s' "$JOIN" | grep -c -- '-e ASM_INTERNAL_ENDPOINT\|AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET')"
OUT="$(run_inject "" "s3cret")"
JOIN="$(join_env "$OUT")"
check "只配 secret ⇒ 不注入任何开关（双开关语义）" "0" "$(printf '%s' "$JOIN" | grep -c -- '-e ASM_INTERNAL_ENDPOINT\|AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET')"
OUT="$(run_inject "" "")"
JOIN="$(join_env "$OUT")"
check "两个都不配 ⇒ 不注入" "0" "$(printf '%s' "$JOIN" | grep -c -- '-e ASM_INTERNAL_ENDPOINT\|AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET')"

# ── 场景⑦：env 文件里必须真的写出去（下一部署还在）────────────────────────
echo "[7] 写入 env 文件（跨部署保留的落点）"
ENVGRP="$(awk '/^\{$/,/^\} > "\$ENV_FILE"$/ { print }' "$ENGINE")"
if [[ "$ENVGRP" == *'export ASM_INTERNAL_ENDPOINT=%q'* ]]; then
  ok "env 文件组里有 ASM_INTERNAL_ENDPOINT 的 printf"
else
  bad "env 文件组里缺 ASM_INTERNAL_ENDPOINT 的 printf"
fi
if [[ "$ENVGRP" == *'export AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=%q'* ]]; then
  ok "env 文件组里有 AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET 的 printf"
else
  bad "env 文件组里缺 AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET 的 printf"
fi

# ── 场景⑧：槽位端口不能再是 8782（被 ASM 的宿主映射占着）──────────────────
echo "[8] 211 槽位 b 端口不再是 8782（ASM 宿主映射占着，实测 EADDRINUSE）"
SLOT_LINE="$(grep -E '^export DD_SLOT_B_PORT=' "$WRAPPER" | head -1)"
DEFAULT_PORT="$(printf '%s' "$SLOT_LINE" | sed -E 's#.*:-([0-9]+)\}.*#\1#')"
check "槽位 b 默认端口 = 8783（上游 §143.56 已把 8782 换成 8783）" "8783" "$DEFAULT_PORT"
if grep -q 'DD_SLOT_B_PORT="${DD_SLOT_B_PORT:-8782}"' "$WRAPPER"; then
  bad "槽位 b 默认端口仍是 8782（与 ASM 的宿主映射冲突）"
else
  ok "槽位 b 默认端口不再是 8782"
fi
# 合同一致性：--dry-run 的 JSON 里的 slot_b_port 必须等于实际默认端口
DRY="$(bash "$WRAPPER" --dry-run 2>/dev/null | head -1)"
DRY_PORT="$(printf '%s' "$DRY" | sed -nE 's/.*"slot_b_port":"([0-9]+)".*/\1/p')"
check "--dry-run 合同里的 slot_b_port = 实际默认端口" "$DEFAULT_PORT" "$DRY_PORT"

echo "=== 结果：通过 ${PASS} / 失败 ${FAIL} ==="
[[ "$FAIL" -eq 0 ]] || exit 1
exit 0
