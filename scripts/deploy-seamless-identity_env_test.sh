#!/usr/bin/env bash
# =============================================================================
# scripts/deploy-seamless-identity_env_test.sh
#
# 判据门：身份网共享密钥在**部署引擎**里的落地与跨部署保留。
#
# 为什么要有这道门（2026-11-03）：
#   网关要给兄弟服务（FreeLLM API）签发单点登录票据，就必须持有
#   IDENTITY_SHARED_SECRET；没有它 /api/auth/app-token fail-closed（503）。
#   而引擎的 env 文件**每次部署都被重写** ⇒ 只在部署机 export 一次，下一次
#   部署就没了 ⇒ 「免登今天能用、明天静默失效」是一个**看不见**的故障
#   （与 ASM 开关同一类）。
#
# 测法：按哨兵注释**抽取引擎里的真代码块**在沙箱里跑 ⇒ 测的是真实现，
#       不是把逻辑复制一份的假实现（复制品会跟引擎一起漂移，测了等于没测）。
#
# 用法: bash scripts/deploy-seamless-identity_env_test.sh
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

echo "=== 判据门：deploy-seamless-identity_env_test ==="

[[ -f "$ENGINE" ]] || { echo "✗ 找不到引擎：$ENGINE"; exit 1; }

# ── 场景①：哨兵成对 + 函数存在 ──────────────────────────────────────────
echo "[1] 结构：哨兵成对、函数存在"
ID_BLK="$(extract_block "$ENGINE" '# ── IDENTITY-ENV-BLOCK:BEGIN ──' '# ── IDENTITY-ENV-BLOCK:END ──')"
# 第 50 增量 F-10/F-11：被测函数体内调用 `_dd_unq`（env 读点共用的 %q 定点解码），
# 沙箱必须先把它定义进来 —— 否则 command substitution 返回空串，用例假红/假绿。
_DD_UNQ_FN="$(sed -n '/^_dd_unq() {/,/^}/p' "$ENGINE")"
if [[ -z "$_DD_UNQ_FN" ]]; then echo "✗ 引擎里提取不到 _dd_unq 函数"; exit 1; fi
INJ_BLK="$(extract_block "$ENGINE" '# ── IDENTITY-ENV-INJECT:BEGIN ──' '# ── IDENTITY-ENV-INJECT:END ──')"
if [[ -n "$ID_BLK" && "$ID_BLK" == *"dd_identity_env()"* ]]; then
  ok "IDENTITY-ENV-BLOCK 哨兵成对且含 dd_identity_env()"
else
  bad "IDENTITY-ENV-BLOCK 哨兵缺失或未含 dd_identity_env()"
fi
if [[ -n "$INJ_BLK" && "$INJ_BLK" == *"IDENTITY_SHARED_SECRET"* ]]; then
  ok "IDENTITY-ENV-INJECT 哨兵成对且含 IDENTITY_SHARED_SECRET"
else
  bad "IDENTITY-ENV-INJECT 哨兵缺失或未含 IDENTITY_SHARED_SECRET"
fi

# ── 场景①b：函数定义必须**先于**调用点 ────────────────────────────────────
# 2026-11-03 实测事故：函数块被放在调用点之后 ⇒ 引擎在写 env 文件那一步
# 直接 `dd_identity_env: command not found` ⇒ 整次部署在最后一步炸掉（镜像
# 已推、容器未切）。本门原先只抽块单跑，**看不到这个顺序问题** ⇒ 这里补上。
echo "[1b] 顺序：定义先于调用"
DEF_LINE="$(grep -n '^dd_identity_env() {' "$ENGINE" | head -1 | cut -d: -f1)"
CALL_LINE="$(grep -n 'dd_identity_env "\$ENV_FILE"' "$ENGINE" | head -1 | cut -d: -f1)"
# ★ 花括号定界：bash 在 UTF-8 locale 下会把全角括号并进变量名
#   （$DEF_LINE） ⇒ 变成一个从未赋值的变量 ⇒ set -u 直接报错。
if [[ -n "${DEF_LINE}" && -n "${CALL_LINE}" && "${DEF_LINE}" -lt "${CALL_LINE}" ]]; then
  ok "dd_identity_env 定义（行 ${DEF_LINE}）在调用（行 ${CALL_LINE}）之前"
else
  bad "dd_identity_env 定义（行 ${DEF_LINE:-无}）未早于调用（行 ${CALL_LINE:-无}）—— 引擎会在写 env 时 command not found"
fi

# ── 沙箱里跑真代码块 ─────────────────────────────────────────────────────
run_identity_env() { # <env_file> <deployer_secret> <deployer_allowlist> <deployer_url>
  (
    set -uo pipefail
    ENV_FILE="$1"
    IDENTITY_SHARED_SECRET="$2"
    IDENTITY_ISSUER_ALLOWLIST="$3"
    FREELLMAPI_PUBLIC_URL="$4"
    log() { :; }
    eval "$_DD_UNQ_FN"
    eval "$ID_BLK"
    dd_identity_env "$ENV_FILE"
    printf '%s\n' "$IDENTITY_SHARED_SECRET"
    printf '%s\n' "$IDENTITY_ISSUER_ALLOWLIST"
    printf '%s\n' "$FREELLMAPI_PUBLIC_URL"
  )
}

# ── 场景②：沿用上次 env 文件里的值（跨部署保留）──────────────────────────
echo "[2] 沿用上次 env 文件（运维改过的那份）"
cat > "$TMP/env.keep" <<'EOF'
# llm-gateway-go @ 211（由 deploy-seamless-docker.sh 生成，勿提交）
export LLM_GATEWAY_STORAGE_MODE=full
export IDENTITY_SHARED_SECRET=kept-shared-secret-value
export IDENTITY_ISSUER_ALLOWLIST=redclaw,memora,llm-gateway,pocket,acc
export FREELLMAPI_PUBLIC_URL=https://freellm.itestu.cn
EOF
OUT="$(run_identity_env "$TMP/env.keep" "" "" "")"
check "shared secret 从旧 env 文件读出" "kept-shared-secret-value" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "allowlist 从旧 env 文件读出"    "redclaw,memora,llm-gateway,pocket,acc" "$(printf '%s\n' "$OUT" | sed -n 2p)"
check "public url 从旧 env 文件读出"   "https://freellm.itestu.cn" "$(printf '%s\n' "$OUT" | sed -n 3p)"

# ── 场景③：部署机环境变量优先于 env 文件里的旧值 ─────────────────────────
echo "[3] 部署机环境变量优先"
OUT="$(run_identity_env "$TMP/env.keep" "deployer-secret" "llm-gateway" "https://from-deployer:9/x")"
check "shared secret 取部署机值" "deployer-secret" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "allowlist 取部署机值"    "llm-gateway" "$(printf '%s\n' "$OUT" | sed -n 2p)"
check "public url 取部署机值"   "https://from-deployer:9/x" "$(printf '%s\n' "$OUT" | sed -n 3p)"

# ── 场景④：bash %q 的引号形态要被剥掉 ────────────────────────────────────
echo "[4] shell 引号形态（bash %q）剥离"
cat > "$TMP/env.quoted" <<'EOF'
export IDENTITY_SHARED_SECRET='quoted-secret'
export IDENTITY_ISSUER_ALLOWLIST='llm-gateway'
export FREELLMAPI_PUBLIC_URL='https://quoted.example.com'
EOF
OUT="$(run_identity_env "$TMP/env.quoted" "" "" "")"
check "shared secret 引号已剥" "quoted-secret" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "allowlist 引号已剥"    "llm-gateway" "$(printf '%s\n' "$OUT" | sed -n 2p)"
check "public url 引号已剥"   "https://quoted.example.com" "$(printf '%s\n' "$OUT" | sed -n 3p)"

# ── 场景⑤：没有 env 文件 ⇒ 三个变量都空（不签发票据）──────────────────────
echo "[5] 无 env 文件 ⇒ 变量为空"
OUT="$(run_identity_env "$TMP/does-not-exist" "" "" "")"
check "shared secret 空" "" "$(printf '%s\n' "$OUT" | sed -n 1p)"
check "allowlist 空"    "" "$(printf '%s\n' "$OUT" | sed -n 2p)"
check "public url 空"   "" "$(printf '%s\n' "$OUT" | sed -n 3p)"

# ── 场景⑥：注入 —— 共享密钥存在才注入 -e ─────────────────────────────────
echo "[6] 注入：共享密钥存在才注入"
run_inject() { # <secret> ⇒ 打印 GW_ENV（元素按行分隔）
  (
    set -uo pipefail
    IDENTITY_SHARED_SECRET="$1"
    IDENTITY_ISSUER_ALLOWLIST="llm-gateway"
    FREELLMAPI_PUBLIC_URL="https://freellm.itestu.cn"
    GW_ENV=()
    log() { :; }
    eval "$INJ_BLK"
    printf '%s\n' "${GW_ENV[@]+"${GW_ENV[@]}"}"
  )
}
# ★ GW_ENV 里每个 `-e KEY=VALUE` 是**两个独立数组元素** ⇒ 断言按 argv 序列看。
join_env() { printf '%s\n' "$1" | tr '\n' ' '; }
OUT="$(run_inject "s3cret-value")"
JOIN="$(join_env "$OUT")"
check "shared secret 被注入" "1" "$(printf '%s' "$JOIN" | grep -c -- '-e IDENTITY_SHARED_SECRET=s3cret-value')"
check "allowlist 被注入"    "1" "$(printf '%s' "$JOIN" | grep -c -- '-e IDENTITY_ISSUER_ALLOWLIST=llm-gateway')"
check "public url 被注入"   "1" "$(printf '%s' "$JOIN" | grep -c -- '-e FREELLMAPI_PUBLIC_URL=https://freellm.itestu.cn')"

OUT="$(run_inject "")"
JOIN="$(join_env "$OUT")"
check "未配共享密钥 ⇒ 不注入任何身份变量" "0" "$(printf '%s' "$JOIN" | grep -c -- '-e IDENTITY_SHARED_SECRET=')"

# ── 场景⑦：env 文件里写回了这三个变量（跨部署保留的落盘面）────────────────
echo "[7] 写回：env 文件落盘三个变量"
WRITE_BLK="$(extract_block "$ENGINE" '# ── GATEWAY-ENV-WRITE:BEGIN ──' '# ── GATEWAY-ENV-WRITE:END ──')"
if [[ "$WRITE_BLK" == *"IDENTITY_SHARED_SECRET=%q"* && "$WRITE_BLK" == *"FREELLMAPI_PUBLIC_URL=%q"* ]]; then
  ok "GATEWAY-ENV-WRITE 块会写回身份网变量"
else
  bad "GATEWAY-ENV-WRITE 块未写回身份网变量"
fi

echo
echo "=== 结果：通过 $PASS / 失败 $FAIL ==="
[[ "$FAIL" -eq 0 ]] || exit 1
