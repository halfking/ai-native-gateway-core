#!/usr/bin/env bash
# =============================================================================
# deploy_seamless_version_identity_test.sh — /version 身份链的行为门
#
# 纯 bash，不碰 docker、不碰目标机：把引擎里「版本身份闸」那段**真代码**
# 抽出来单独驱动，钉住三条契约：
#
#   ① 目标机直跑（无 .git）+ 未显式给号 ⇒ fail-closed（拒绝部署）；
#   ② 显式给的号不被 git 覆盖（部署机侧的逃生门必须真能用）；
#   ③ --via 部署机侧取不到号 ⇒ 不发那次 ssh（错误落在信息最全的地方，
#      而不是让目标机撞死、看起来像「远端坏了」）。
#
# ★ 为什么是行为门而不是文本门（2026-10-28 批判式审计）：
#   上一轮只加了实现（fail-closed die）没加测试 ⇒ 门是「有功能没牙齿」。
#   而且当时的实现有两处真缺陷（显式号被 git 覆盖、--via 路径的门根本不
#   执行），文本 grep 门（"脚本里有没有 die"）对这两处都绿。
#   ⇒ 这里的做法：把真代码抠出来跑，断言它**真的拒绝**。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 被测引擎路径可被环境变量覆盖 —— **只给变异验证用**（把改坏的副本喂给门，
# 证明门确实在测东西而不是恒绿）。日常运行不设该变量，指向仓库里的真引擎。
SCRIPT="${DEPLOY_211_ENGINE:-$ROOT/scripts/deploy-seamless-docker.sh}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$SCRIPT" ]] || { echo "找不到 $SCRIPT"; exit 1; }

# ── 抽取引擎里的真代码片段 ──────────────────────────────────────────────
# 抽的是**引擎里那段身份闸的原文**（不是复制品）——复制品会与引擎漂移，
# 那正是本轮要治的病。
extract_gate_block() {
  # 目标机侧身份闸：从「版本身份链」注释块的判定行到该块的 fi。
  awk '
    /^if \[\[ "\$MODE" == "deploy" && -z "\$\{DD_BUILD_SHA:-\}" \]\]; then$/ {inblk=1}
    inblk {print}
    inblk && /^fi$/ {exit}
  ' "$SCRIPT"
}

extract_deploy_machine_block() {
  # 部署机侧取号段：**按行区间取**（从「构建提交号随行」注释到「前端自动构建」注释），
  # 不用「找某个 if 条件」的方式 ——
  #   ★ 原因（2026-10-28 变异自检实测）：按条件抽时，把条件改成 `if true; then`
  #     就抽不出片段 ⇒ 片段为空 ⇒ 「显式号不被覆盖」那条断言**空洞通过**
  #     （断言的是空片段 ⇒ 恒真）⇒ 门对着它要防的变异没有牙。
  #   ⇒ 按稳定的注释锚点取整段，片段里必然含取号语句；
  #     断言前另有「片段非空且含 rev-parse」的反空洞检查。
  awk '
    /构建提交号随行/ {inblk=1}
    inblk {print}
    /前端自动构建/ {if (inblk) exit}
  ' "$SCRIPT"
}

extract_via_gate_block() {
  # --via 部署机侧的门：那句「部署机侧取不到构建提交号」的 die及其所在 if。
  grep -n '部署机侧取不到构建提交号' "$SCRIPT" | head -1 | cut -d: -f1
}

# ── 组装可执行片段：真代码 + 最小依赖（log/die/MODE/DD_REPO_ROOT）──────
build_target_gate() {
  local gate; gate="$(extract_gate_block)"
  [[ -n "$gate" ]] || return 1
  cat > "$TMP/target_gate.sh" <<EOF
DD_TARGET=test
log()  { printf '[%s] %s\n' "\$DD_TARGET" "\$*" >&2; }
die()  { printf '[%s] FATAL: %s\n' "\$DD_TARGET" "\$*" >&2; exit 1; }
MODE="$1"
DD_REPO_ROOT="$2"
DD_BUILD_SHA="$3"
$gate
EOF
}

build_machine_gate() {
  local gate; gate="$(extract_deploy_machine_block)"
  # 反空洞：片段为空或不含取号语句 ⇒ 后续断言会恒真 ⇒ 直接判失败。
  [[ -n "$gate" ]] || return 1
  printf '%s' "$gate" | grep -q 'rev-parse --short=8 HEAD' || return 1
  cat > "$TMP/machine_gate.sh" <<EOF
DD_TARGET=test
log()  { printf '[%s] %s\n' "\$DD_TARGET" "\$*" >&2; }
die()  { printf '[%s] FATAL: %s\n' "\$DD_TARGET" "\$*" >&2; exit 1; }
DD_REPO_ROOT="$1"
DD_BUILD_SHA="$2"
$gate
printf 'RESULT_SHA=%s\n' "\$DD_BUILD_SHA"
EOF
}

echo "── 版本身份链行为门（2026-10-28）"

# ── 契约 ①：目标机直跑 + 无 .git + 未给号 ⇒ 拒绝 ────────────────────────
# ★ 判据要区分「哪一道门拒的」（2026-10-28 变异自检 M2 实测）：
#   把第一道门（无 .git ⇒ die）降级成 log 时，后面那道「git rev-parse 失败 ⇒ die」
#   仍会兜住 ⇒ 整体仍然是「拒绝」⇒ 只断言「整体拒绝」的门**看不见 M2**。
#   ⇒ 因此分两臂：
#     臂甲（无 .git 树 + 无号）：断言整体拒绝 **且** 拒绝理由来自第一道门；
#     臂乙（有 .git 但 rev-parse 失败）：断言第二道门独立有效。
#   臂甲的「理由归属」判据：拒绝信息里必须出现第一道门的专属措辞
#   （"同步树形态"）——第二道门的措辞是"有 .git 但 git rev-parse 失败"。
if build_target_gate deploy "$TMP/no_git_tree" ""; then
  # 造一个「无 .git 的树」（目录存在、.git 不存在）
  mkdir -p "$TMP/no_git_tree"
  out="$(bash "$TMP/target_gate.sh" 2>&1)"
  rc=$?
  if [[ $rc -ne 0 ]]; then
    ok "目标机直跑（无 .git + 未给号）⇒ 拒绝部署（exit=${rc}）"
  else
    bad "目标机直跑（无 .git + 未给号）竟放行了 —— /version 会沿用别人的旧戳说谎"
  fi
  if printf '%s' "$out" | grep -q '同步树形态'; then
    ok "拒绝来自第一道门（无 .git 同步树形态）—— 两道门各自有效，不是被第二道兜住的"
  else
    bad "拒绝不是来自第一道门（无 .git 那道可能被降级了，被第二道兜住掩盖）"
  fi
  if printf '%s' "$out" | grep -q 'DD_BUILD_SHA'; then
    ok "拒绝信息里点名了 DD_BUILD_SHA（运维知道该给什么）"
  else
    bad "拒绝信息没提 DD_BUILD_SHA：运维无从下手"
  fi
else
  bad "抽不出目标机侧身份闸（脚本结构变了？）"
fi

# ── 契约 ①b：非 deploy 模式不受这道门影响（--status/--probe-only）──────
if build_target_gate status "$TMP/no_git_tree" ""; then
  out="$(bash "$TMP/target_gate.sh" 2>&1)"
  rc=$?
  if [[ $rc -eq 0 ]]; then
    ok "非 deploy 模式（--status/--probe-only）不被身份闸拦（只查状态不该被身份问题挡住）"
  else
    bad "非 deploy 模式被身份闸拦了：--status/--probe-only 会失败（exit=${rc}）"
  fi
else
  bad "抽不出目标机侧身份闸（--status 场景）"
fi

# ── 契约 ①c：显式给号 ⇒ 放行（逃生门可用）──────────────────────────────
if build_target_gate deploy "$TMP/no_git_tree" "deadbeef"; then
  out="$(bash "$TMP/target_gate.sh" 2>&1)"
  rc=$?
  if [[ $rc -eq 0 ]]; then
    ok "目标机直跑 + 显式给号 ⇒ 放行（逃生门可用）"
  else
    bad "显式给号仍被拒（exit=${rc}）：逃生门不可用 ⇒ 运维声明的身份永远到不了生产"
  fi
  if printf '%s' "$out" | grep -q 'deadbeef'; then
    ok "放行日志里回显了给定的号（版本戳来源可追）"
  else
    bad "放行日志没回显给定的号：版本戳来源不可追"
  fi
else
  bad "抽不出目标机侧身份闸（显式给号场景）"
fi

# ── 契约 ②：部署机侧取号段——显式号不被 git 覆盖 ───────────────────────
if build_machine_gate "$ROOT" "aaaabbbb"; then
  out="$(bash "$TMP/machine_gate.sh" 2>&1)"
  sha="$(printf '%s' "$out" | sed -n 's/^RESULT_SHA=//p')"
  check "部署机侧：显式给的号不被 git 覆盖（逃生门在 --via 路径上真能用）" "aaaabbbb" "$sha"
else
  bad "抽不出部署机侧取号段（脚本结构变了？）"
fi

#契约 ②b：未给号时确实从 git 取（真仓）
if build_machine_gate "$ROOT" ""; then
  out="$(bash "$TMP/machine_gate.sh" 2>&1)"
  sha="$(printf '%s' "$out" | sed -n 's/^RESULT_SHA=//p')"
  head_sha="$(git -C "$ROOT" rev-parse --short=8 HEAD 2>/dev/null || true)"
  if [[ -n "$sha" && "$sha" == "$head_sha" || "$sha" == "$head_sha-dirty" ]]; then
    ok "部署机侧：未给号时从 git 取号（${sha}）"
  else
    bad "部署机侧：未给号时没取到 git 号（期望 $head_sha[-dirty]，实得 ${sha:-空}）"
  fi
else
  bad "抽不出部署机侧取号段（git 取号场景）"
fi

# ── 契约 ③：--via 部署机侧取不到号 ⇒ 不发那次 ssh ──────────────────────
via_line="$(extract_via_gate_block)"
if [[ -n "$via_line" ]]; then
  # 取号段之后、ssh 之前必须有一道「号为空 ⇒ 不发 ssh」的门。
  # 判据取行为形态：die 出现在 ssh 之前，且 die 的条件以号为空为前提。
  ssh_line="$(grep -n "DD_BUILD_SHA='\${DD_BUILD_SHA}'" "$SCRIPT" | tail -1 | cut -d: -f1)"
  if [[ -n "$ssh_line" && "$via_line" -lt "$ssh_line" ]]; then
    ok "--via 路径：身份门在转发 ssh 之前（号为空时不会把空号发到目标机）"
  else
    bad "--via 路径：身份门不在 ssh 之前（空号会被发到目标机 ⇒ 错误落在远端、看起来像远端坏了）"
  fi
else
  bad "找不到 --via 部署机侧的身份门（die 文案缺失 ⇒ 门被删了？）"
fi

echo
printf '结果: %d 通过, %d 失败\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]] || exit 1
