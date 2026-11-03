#!/usr/bin/env bash
# 元判据门：验证 goacos 判据门**本身**能红，而且红得**快**。
#
# ★ 为什么要有这条（2026-11-03 一次「门挂起」事故驱动）：
#   判据门最危险的失败形态不是「红」，而是「**挂起**」——
#   挂起时既没有红字也没有绿字，在 CI 里和「通过」长得一模一样。
#   那天真实发生了一次：变异验证跑满 300 秒被外部超时掐掉，输出只有
#   "mutation applied: ..."。⇒ 门「能红」这件事本身必须被机器校验，
#   而且「红」必须**在限定时间内**发生（挂起 = 最严重失败）。
#
# ★ 三条断言：
#   ① 基线：真实引擎 ⇒ 绿，且输出含 OK goacos register contract holds；
#   ② 变异①：去掉 data.hosts 回落分支 ⇒ 红，红字必须是
#      "nested shape: stale DELETE must still happen"（证明场景④b 有牙）；
#   ③ 变异②：退回修复前（只读 data.hosts）⇒ 红，红字必须是
#      "expected exactly one stale DELETE"（证明场景① 有牙 ⇒ 它能抓住
#      **真机上的那个生产缺陷**：顶层形状下旧条目不注销）。
#
# 每次运行都带时限（MUTATION_BOUND，默认 120s）⇒ 门挂起 ⇒ 本门变红。
set -euo pipefail
self=$(cd "$(dirname "$0")" && pwd)
engine=${ENGINE:-$self/deploy-seamless-docker.sh}
gate=${GATE:-$self/deploy-seamless-goacos_test.sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp" 2>/dev/null || true' EXIT

bound=${MUTATION_BOUND:-120}

die() { echo "$*" >&2; exit 1; }

[[ -f "$engine" ]] || die "missing engine: $engine"
[[ -f "$gate" ]] || die "missing gate: $gate"

# ---- 变异工具：按语义改引擎副本，绝不碰真引擎 ----------------------------
cat > "$tmp/mutate.py" <<'PY'
import sys

src, dst, mode = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(src, encoding="utf-8").read()

if mode == "drop_fallback":
    old = """    if not hosts and isinstance(_doc.get("data"), dict):
        hosts = _doc["data"].get("hosts") or []"""
    new = ""
elif mode == "legacy_only_data_hosts":
    old = """    hosts = _doc.get("hosts") or []
    if not hosts and isinstance(_doc.get("data"), dict):
        hosts = _doc["data"].get("hosts") or []"""
    new = """    hosts = _doc.get("data", {}).get("hosts") or []"""
else:
    raise SystemExit("unknown mutation mode: %s" % mode)

if old not in s:
    raise SystemExit("mutation anchor not found in %s (engine changed?)" % src)
s = s.replace(old, new, 1)
open(dst, "w", encoding="utf-8").write(s)
PY

# ---- 限时运行：RC / TIMED_OUT --------------------------------------------
RC=0
TIMED_OUT=0
run_bounded() {
  local eng="$1" out="$2"
  local flag="$tmp/timed_out.$RANDOM"
  rm -f "$flag"
  RC=0
  TIMED_OUT=0
  ( ENGINE="$eng" bash "$gate" ) >"$out" 2>&1 &
  local job=$!
  # ★ 同一条纪律：看门狗用**单个** python 进程（不是 `( sleep; ... ) &` 包装），
  #   否则包装层被杀后 sleep 变孤儿、继续持有管道 ⇒ 调用方 `门 | tail` 挂死。
  python3 - "$bound" "$flag" "$job" >/dev/null 2>&1 <<'PY' &
import os, sys, time
time.sleep(float(sys.argv[1]))
try:
    open(sys.argv[2], "w").close()
except OSError:
    pass
try:
    os.kill(int(sys.argv[3]), 15)
except OSError:
    pass
PY
  local wd=$!
  wait "$job" || RC=$?
  kill "$wd" 2>/dev/null || true
  wait "$wd" 2>/dev/null || true
  if [[ -f "$flag" ]]; then TIMED_OUT=1; fi
  return 0
}

assert_not_hung() {
  [[ "$TIMED_OUT" == "0" ]] \
    || die "$1：判据门在 ${bound}s 内没有退出（挂起）⇒ 门失效（挂起必须判红，不能无声卡死）"
}

assert_red_with() {
  local name="$1" expect="$2" out="$3"
  [[ "$RC" != "0" ]] \
    || die "$1：变异后判据门仍然**绿**（退出码 0）⇒ 这条断言没有牙（期望红字：$expect）"
  grep -qF "$expect" "$out" \
    || die "$1：判据门红了，但红字不是期望的那条（期望包含：$expect）"
}

# ---- ① 基线：真实引擎必须绿 ----------------------------------------------
run_bounded "$engine" "$tmp/base.out"
assert_not_hung "基线"
[[ "$RC" == "0" ]] || die "基线：真实引擎下判据门不是绿的（退出码 $RC）—— 门本身坏了"
grep -q "OK goacos register contract holds" "$tmp/base.out" \
  || die "基线：判据门没有输出 OK goacos register contract holds"
echo "PASS ① 基线：真实引擎 ⇒ 绿（退出码 0，限时内）"

# ---- ② 变异①：去掉 data.hosts 回落分支 ---------------------------------
python3 "$tmp/mutate.py" "$engine" "$tmp/engine_m1.sh" drop_fallback
run_bounded "$tmp/engine_m1.sh" "$tmp/m1.out"
assert_not_hung "变异①"
assert_red_with "变异①" "nested shape: stale DELETE must still happen" "$tmp/m1.out"
echo "PASS ② 变异①（去掉 data.hosts 回落）⇒ 红：nested shape: stale DELETE must still happen"

# ---- ③ 变异②：退回修复前（只读 data.hosts）------------------------------
python3 "$tmp/mutate.py" "$engine" "$tmp/engine_m2.sh" legacy_only_data_hosts
run_bounded "$tmp/engine_m2.sh" "$tmp/m2.out"
assert_not_hung "变异②"
assert_red_with "变异②" "expected exactly one stale DELETE" "$tmp/m2.out"
echo "PASS ③ 变异②（退回只读 data.hosts）⇒ 红：expected exactly one stale DELETE"

echo "OK goacos 判据门的红/绿能力成立（且不会挂起）"
