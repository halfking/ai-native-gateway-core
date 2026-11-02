#!/usr/bin/env bash
# =====================================================================
# tests/deploy_force_lock_recovery_test.sh — 211 部署的 --force 锁恢复契约
#
# ★ 2026-11-02 第二十轮（F-21）：
#
#   兄弟档 F21 的登记内容是「崩溃的部署泄漏锁目录，恢复靠手工 rm -rf；
#   且 --force 无效（F20）」⇒ 第二十轮查实：**只对一半**。
#   · 库层 scripts/deploy-lib/lock.sh **已有** lock_recover_local(target, force)
#     （校验 target / metadata / 持有者 pid 存活，活着的持有者一律拒删）；
#   · 本地入口 deploy-seamless.sh **已有** --force 接线；
#   · ★ **缺口**：Docker 形态的**共享引擎** deploy-seamless-docker.sh
#     **没有** --force（"force" 零命中）⇒ 而 scripts/deploy-211.sh 用
#     extract_force_unlock 把 --force **剥掉**（免得引擎报 unknown argument）
#     ⇒ 211 部署卡在 rc=75 时，运维的 --force **到不了引擎** ⇒ 只能手工
#     rm -rf /tmp/kx-llm-gateway-deploy-211.lock。
#
#   本轮修法：引擎加 **默认关** 的 --force（先恢复、后获取），
#   deploy-211.sh 按 154/245 同款模式把 --force **加回** ARGS 转交引擎。
#
# 覆盖：
#   AC-1  引擎接受 --force（不再 unknown argument）。
#   AC-2  ★ 顺序是硬的：恢复调用在 lock_acquire_local **之前**。
#   AC-3  ★ 默认关：FORCE_LOCK_RECOVER 缺省 0，且只在 --force 分支置 1
#         ⇒ ★ 不传 --force 的既有调用方（192.168.31.32 的自动化）行为不变。
#   AC-4  --force **不转发**给远端（它是部署机侧的本地动作）。
#   AC-5  deploy-211.sh 把 --force **加回** ARGS（转交引擎），
#         且不再宣称「--force 被忽略」。
#   AC-6  lock_recover_local 接受 target 211 ⇒ 死持有者 ⇒ 恢复成功。
#   AC-7  ★ 活着的持有者仍被保护（不删、rc≠0）—— 本轮**没有放松安全性**。
#   AC-8  跨 target 仍拒删（211 的恢复不得动 154 的锁）。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE="$REPO_ROOT/scripts/deploy-seamless-docker.sh"
SHELL_211="$REPO_ROOT/scripts/deploy-211.sh"
LIB_LOCK="$REPO_ROOT/scripts/deploy-lib/lock.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); }
bad() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }

[[ -f "$ENGINE" ]] || { echo "missing $ENGINE" >&2; exit 1; }
[[ -f "$SHELL_211" ]] || { echo "missing $SHELL_211" >&2; exit 1; }
[[ -f "$LIB_LOCK" ]] || { echo "missing $LIB_LOCK" >&2; exit 1; }

# ── AC-1 / AC-3 / AC-4：引擎的参数处理 ────────────────────────────────

grep -q -- '--force)' "$ENGINE" && ok || bad "引擎不接受 --force（参数循环里没有 --force 分支）"

# ★ 默认关：变量缺省为 0，且只有 --force 分支能把它置 1。
if grep -qE '^FORCE_LOCK_RECOVER=0$' "$ENGINE"; then
  ok
else
  bad "FORCE_LOCK_RECOVER 的缺省值不是 0 ⇒ 默认关这条不成立"
fi
setter_count=$(grep -cE 'FORCE_LOCK_RECOVER=1' "$ENGINE")
if [[ "$setter_count" == "1" ]]; then
  ok
else
  bad "FORCE_LOCK_RECOVER=1 出现了 $setter_count 次（应当只有 --force 分支这一处）"
fi

# ★ --force 不转发给远端（转发集合是 DD_ARGS）。
if grep -E -- '--force\)' "$ENGINE" | grep -q 'DD_ARGS'; then
  bad "--force 被放进了 DD_ARGS ⇒ 会被转发给远端（它是部署机侧的本地动作）"
else
  ok
fi

# ── AC-2：顺序（先恢复、后获取）─────────────────────────────────────────

# ★ 判据必须与它自称的一致：这里比的是**真正的调用行**，不是注释里提到的名字。
#   ⇒ 锚定「行首 = lock_recover_local "$DD_TARGET"」（注释行以 # 开头 ⇒ 不匹配）。
#   ⇒ 第一版写的是 `grep -nE 'lock_recover_local[[:space:]"]' | head -1`
#      ⇒ ★ 它匹到了**注释里**的那一行 ⇒ 变异臂 M104（把恢复调用挪到获取之后）
#      竟然没红 ⇒ **前提为假的门**（正是仓内 LESSONS 点名的失败模式）⇒ 已修。
recover_line=$(grep -nE '^[[:space:]]*lock_recover_local[[:space:]]+"\$DD_TARGET"' "$ENGINE" | head -1 | cut -d: -f1)
acquire_line=$(grep -nE '^[[:space:]]*if ! lock_acquire_local' "$ENGINE" | head -1 | cut -d: -f1)
if [[ -n "$recover_line" && -n "$acquire_line" ]] && [[ "$recover_line" -lt "$acquire_line" ]]; then
  ok
else
  bad "恢复调用（行 ${recover_line:-?}）必须在 lock_acquire_local（行 ${acquire_line:-?}）**之前**"
fi
if [[ -n "$recover_line" ]] && grep -qE '^[[:space:]]*#.*lock_recover_local' "$ENGINE"; then
  : # 注释里提到它很正常（说明为什么这么排）⇒ 不算问题
fi

# ── AC-5：211 外壳把 --force 转交引擎 ──────────────────────────────────

if grep -q 'ARGS+=(--force)' "$SHELL_211"; then
  ok
else
  bad "deploy-211.sh 没有把 --force 加回 ARGS ⇒ --force 仍然到不了引擎"
fi
if grep -q -- '--force 被忽略' "$SHELL_211"; then
  bad "deploy-211.sh 仍宣称「--force 被忽略」⇒ 那句话现在是错的"
else
  ok
fi

# ── AC-6 / AC-7 / AC-8：lock_recover_local 的真实行为 ──────────────────
#
# ★ 在**子 shell**里 source 锁库（锁库会改 shell 选项，且要独立的 LOCK_TMP_ROOT）。

# AC-6：target 211 + 死持有者 ⇒ 恢复成功（目录消失）。
out=$(LOCK_TMP_ROOT="$TMP" bash -c '
  set -uo pipefail
  source "$1"
  lock_dir="$LOCK_TMP_ROOT/kx-llm-gateway-deploy-211.lock"
  mkdir -p "$lock_dir"
  printf "target=211\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n" >"$lock_dir/metadata"
  lock_recover_local 211 1
  rc=$?
  if [[ -e "$lock_dir" ]]; then echo "STILL_THERE"; else echo "GONE"; fi
  exit $rc
' _ "$LIB_LOCK" 2>&1)
if [[ "$out" == *GONE* ]] && ! echo "$out" | grep -q 'invalid lock target'; then
  ok
else
  bad "target 211 的陈旧锁没有被恢复：$out"
fi

# AC-7：活着的持有者 ⇒ 拒删（目录还在 + rc 非 0）—— 本轮没有放松安全性。
out=$(LOCK_TMP_ROOT="$TMP" bash -c '
  set -uo pipefail
  source "$1"
  sleep 30 &
  live_pid=$!
  trap "kill $live_pid 2>/dev/null || true" EXIT
  lock_dir="$LOCK_TMP_ROOT/kx-llm-gateway-deploy-211.lock"
  mkdir -p "$lock_dir"
  printf "target=211\npid=%s\n" "$live_pid" >"$lock_dir/metadata"
  rc=0
  lock_recover_local 211 1 || rc=$?
  if [[ -d "$lock_dir" ]]; then echo "KEPT rc=$rc"; else echo "REMOVED rc=$rc"; fi
' _ "$LIB_LOCK" 2>&1)
if echo "$out" | grep -q 'KEPT rc=' && ! echo "$out" | grep -q 'rc=0'; then
  ok
else
  bad "活着的持有者没有被保护（应拒删且 rc≠0）：$out"
fi

# AC-8：跨 target ⇒ 别的 target 的锁**结构上**不可能被动到。
# ★ lock_recover_local 的操作路径**由 target 推导**
#   （${LOCK_TMP_ROOT}/kx-llm-gateway-deploy-<target>.lock）⇒ 它连 154 的
#   路径都不会打开 ⇒ 保护是**结构性**的，不依赖 metadata 比对。
#   ⇒ 正确行为：154 的锁原封不动，且恢复 211 时**不产生任何副作用**。
out=$(LOCK_TMP_ROOT="$TMP" bash -c '
  set -uo pipefail
  source "$1"
  lock_dir="$LOCK_TMP_ROOT/kx-llm-gateway-deploy-154.lock"
  mkdir -p "$lock_dir"
  printf "target=154\npid=999999\n" >"$lock_dir/metadata"
  rc=0
  lock_recover_local 211 1 || rc=$?
  if [[ -d "$lock_dir" ]]; then echo "KEPT rc=$rc"; else echo "REMOVED rc=$rc"; fi
' _ "$LIB_LOCK" 2>&1)
if echo "$out" | grep -q 'KEPT' && ! echo "$out" | grep -q 'REMOVED'; then
  ok
else
  bad "跨 target 的锁被误删：$out"
fi

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
