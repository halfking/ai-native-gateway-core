#!/usr/bin/env bash
# =====================================================================
# tests/bump_version_collision_test.sh — bump-version.sh 机器级 floor 计数器
#
# 锁定的行为（2026-09-26，owner 决策②）：
#   1. 两个并行 checkout 同读 build_seq=2249、并发 bump，必须得到**不同**
#      的新 seq（2250/2251）—— 09-25 seq 2250 撞号实录的回归钉子：撞号会
#      产出同名 release 目录/镜像 tag，互相覆盖后 cutover 可能切到对方的
#      二进制。
#   2. floor 计数器持久：--seq 显式指定推进 floor，后续默认 bump 严格在其上。
#   3. floor 不可用时（目录建不出来）静默退回旧行为（本地 seq +1），不阻塞。
#   4. 全新机器（floor 文件不存在）首个 bump 保持连续性：max(0, current)+1。
#
# offline-only：全部在 mktemp 沙箱里跑，不触网、不碰真实仓库文件。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0; FAIL=0
ok()   { printf 'PASS %s\n' "$1"; ((PASS++)); }
bad()  { printf 'FAIL %s\n' "$1" >&2; ((FAIL++)); }
SUMMARY() { printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"; [[ $FAIL -eq 0 ]]; }

# 最小假 checkout：只带 bump-version.sh 和版本文件，git init 供 tag/sha 回退
make_fake_checkout() { # $1=dir $2=build_seq
  local dir="$1" seq="$2"
  mkdir -p "$dir/scripts" "$dir/web/public"
  cp "$REPO_ROOT/scripts/bump-version.sh" "$dir/scripts/"
  printf 'fake-%s\n' "$seq" > "$dir/VERSION"
  cat > "$dir/version.json" <<EOF
{"version": "9.9.9-fake-$seq", "git_tag": "9.9.9", "git_sha": "fake0000", "build_seq": $seq, "build_date": "20260926", "module": "llm-gateway-go"}
EOF
  cp "$dir/version.json" "$dir/web/public/version.json"
  git init -q "$dir"
}

seq_of() { # $1=checkout dir → bump 输出的 target seq
  (cd "$1" && bash scripts/bump-version.sh 2>&1) | sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p'
}

SANDBOX="$(mktemp -d /tmp/bump-collision-test.XXXXXX)"
trap 'rm -rf "$SANDBOX"' EXIT

# ── 1. 并行双 checkout 同起点并发 bump → seq 必须不同 ──────────────
A="$SANDBOX/a"; B="$SANDBOX/b"
make_fake_checkout "$A" 2249
make_fake_checkout "$B" 2249
export LLM_GATEWAY_BUILD_SEQ_FLOOR_DIR="$SANDBOX/floor"
# 并发执行、各自落盘捕获输出（后台赋值拿不到子 shell 变量，用文件中转）
fA="$SANDBOX/out.a"; fB="$SANDBOX/out.b"
(cd "$A" && bash scripts/bump-version.sh 2>&1) > "$fA" &
wa=$!
(cd "$B" && bash scripts/bump-version.sh 2>&1) > "$fB" &
wb=$!
wait "$wa"; wait "$wb"
got_a="$(sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p' "$fA")"
got_b="$(sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p' "$fB")"
if [[ "$got_a" =~ ^[0-9]+$ && "$got_b" =~ ^[0-9]+$ && "$got_a" != "$got_b" \
   && $(( got_a > 2249 )) -eq 1 && $(( got_b > 2249 )) -eq 1 ]]; then
  ok "并行双 checkout 同起点并发 bump 得到不同 seq (a=$got_a b=$got_b)"
else
  bad "并行双 checkout 撞号回归：a='$got_a' b='$got_b'（期望不同且均 >2249）"
fi
# 两次的 seq 集合应恰为 {2250, 2251}（每个只消费一次，无空洞无重复）
if [[ ((" $got_a $got_b " == *" 2250 "*) && (" $got_a $got_b " == *" 2251 "*)) ]]; then
  ok "并发消费连续无重复 (2250/2251 各一次)"
else
  bad "并发消费异常：期望 {2250,2251}，实际 {$got_a,$got_b}"
fi

# ── 2. floor 持久：--seq 推进后默认 bump 严格在其上 ────────────────
out="$( (cd "$A" && bash scripts/bump-version.sh --seq 3000 2>&1) )"
s1="$(printf '%s\n' "$out" | sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p')"
s2="$(seq_of "$B")"
if [[ "$s1" == "3000" && "$s2" == "3001" ]]; then
  ok "floor 持久：--seq 3000 后下一个默认 bump = 3001"
else
  bad "floor 持久性破坏：--seq 结果='$s1' 期望 3000；后续 bump='$s2' 期望 3001"
fi

# ── 3. floor 不可用 → 退回旧行为，不阻塞 ───────────────────────────
C="$SANDBOX/c"
make_fake_checkout "$C" 2249
touch "$SANDBOX/blocker"   # 普通文件：其下不能再建目录 → mkdir -p 必败
out="$( (cd "$C" && LLM_GATEWAY_BUILD_SEQ_FLOOR_DIR="$SANDBOX/blocker/sub" \
  bash scripts/bump-version.sh 2>&1) )"
s3="$(printf '%s\n' "$out" | sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p')"
if [[ "$s3" == "2250" ]]; then
  ok "floor 不可用退回旧行为：2249 -> 2250"
else
  bad "floor 不可用退化异常：期望 2250，得到 '$s3'"
fi

# ── 4. 全新机器连续性：floor 文件缺失 → max(0, current)+1 ─────────
D="$SANDBOX/d"
make_fake_checkout "$D" 500
out="$( (cd "$D" && LLM_GATEWAY_BUILD_SEQ_FLOOR_DIR="$SANDBOX/fresh" \
  bash scripts/bump-version.sh 2>&1) )"
s4="$(printf '%s\n' "$out" | sed -n 's/.*target:  seq=\([0-9]*\).*/\1/p')"
if [[ "$s4" == "501" ]]; then
  ok "全新机器连续性：current=500 且 floor 缺失 -> 501"
else
  bad "全新机器连续性破坏：期望 501，得到 '$s4'"
fi

SUMMARY
