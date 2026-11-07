#!/usr/bin/env bash
# scripts/deploy-seamless-dd_unq_guard_test.sh —— _dd_unq（%q 定点解码）守卫门。
#
# 第 50 增量审计（2026-11-07）C 组发现：_dd_unq 用 eval 做 %q 逆变换，对
# 「反斜杠 + 未转义 $(...) / 反引号」混合形态会**当场执行命令替换**
# （沙箱实测 touch 落地）；修法＝eval 前危险形态守卫（剥掉全部 `\$` 对后
# 剩任何未转义 `$(`/`${`/反引号/非 `$'` 的 `$` ⇒ 拒绝解码原样返回）。
# 本门把「守卫有牙 + 合法栈照常解码」钉成常设判据（首版守卫的
# `printf '%c' 96 → '9'` 反引号漏判就是被这里的注入矩阵当场抓回来的）。
#
# 用法: bash scripts/deploy-seamless-dd_unq_guard_test.sh [engine]
set -uo pipefail
ENGINE="${1:-scripts/deploy-seamless-docker.sh}"
PASS=0; FAIL=0
ok()  { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1（期望 [$2] 实际 [$3]）"; fi; }

[[ -f "$ENGINE" ]] || { echo "✗ 找不到引擎：$ENGINE"; exit 2; }
DD_FN="$(sed -n '/^_dd_unq() {/,/^}/p' "$ENGINE")"
[[ -n "$DD_FN" ]] || { echo "✗ 引擎里提取不到 _dd_unq() —— 函数被改名/搬走，读侧解码链断了"; exit 2; }

# ── [1] 结构：_dd_unq 定义必须先于第一个 env 读点（HARD_BLOCK）──────────────
# bash 函数「先定义后调用」：三个 inline 读点（HARD_BLOCK/TRUSTED/PROBE_LANE）
# 在顶层就地执行，函数若定义在其后 ⇒ 部署时 command not found ⇒ 解码链整条断裂。
FN_LINE="$(grep -n '^_dd_unq() {' "$ENGINE" | head -1 | cut -d: -f1)"
FIRST_READ_LINE="$(grep -n '^HARD_BLOCK_RULES=' "$ENGINE" | head -1 | cut -d: -f1)"
if [[ -n "$FN_LINE" && -n "$FIRST_READ_LINE" && "$FN_LINE" -lt "$FIRST_READ_LINE" ]]; then
  ok "_dd_unq 定义（行 ${FN_LINE}）先于第一个读点 HARD_BLOCK（行 ${FIRST_READ_LINE}）"
else
  bad "_dd_unq 定义（行 ${FN_LINE:-无}）必须早于 HARD_BLOCK 读点（行 ${FIRST_READ_LINE:-无}）"
fi

# ── 沙箱 ────────────────────────────────────────────────────────────────────
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
run_unq() { # <value> ⇒ stdout=解码输出；副作用探针文件=$TMP/pwned
  (
    set -uo pipefail
    eval "$DD_FN"
    _dd_unq "$1"
  )
}

# ── [2] 注入矩阵：混合形态必须**原样返回 + 零副作用**（守卫有牙）────────────
echo "[2] 注入矩阵（eval 前危险形态守卫）"
PWN="$TMP/pwned"
inj() { # <label> <raw value>
  rm -f "$PWN"
  out="$(run_unq "$2")" || true
  if [[ -e "$PWN" ]]; then bad "$1：命令替换被执行了（touch 落地）—— 守卫漏判"; else ok "$1：未执行"; fi
  check "$1：原样返回（拒绝解码）" "$2" "$out"
}
inj '命令替换  $( )' 'a\b$(touch '"$PWN"')'
inj '命令替换  反引号' "a\\b\`touch $PWN\`"
inj '参数展开  ${ }' 'a\b${HOME}'
inj '变量展开  $VAR' 'a\b$HOME'
inj '算术展开  $(( ))' 'a\b$((1+1))'

# ── [3] 合法解码矩阵：%q 栈照常定点解码 ────────────────────────────────────
echo "[3] 合法解码矩阵"
enc() { local v=$1; shift; local i; for i in $(seq "${1:-1}"); do v=$(printf %q "$v"); done; printf '%s' "$v"; }
check '单层 %q（含空格）'        "has space"  "$(run_unq "$(enc 'has space' 1)")"
check '双层累积'                 "has space"  "$(run_unq "$(enc 'has space' 2)")"
check '五层累积（211 存量形态）' "issuer-list real" "$(run_unq "$(enc 'issuer-list real' 5)")"
check '裸 $() 值不进 eval（无反斜杠）' '$(not-executed)' "$(run_unq '$(not-executed)')"
check '普通值原样'               'plain value' "$(run_unq 'plain value')"
check '空串'                     ''            "$(run_unq '')"
check '合法 \$ 字面量一层解码'   '$(id)'       "$(run_unq "$(printf %q '$(id)')")"
out="$(run_unq "$(printf %q "$(printf %q '$(id)')")")"
check '两层包装的 $(id) 解到一层' '$(id)' "$out"

echo
echo "=== 结果：通过 $PASS / 失败 $FAIL ==="
(( FAIL == 0 )) || exit 1
exit 0
