#!/usr/bin/env bash
# =====================================================================
# tests/deploy_test_gate_contract_test.sh — 部署前测试闸门的参数契约 + worktree 残留清扫
#
# ★ 2026-11-02 第十九轮（F-22 / F-23）：
#
#   F-23  `scripts/deploy-test-gate.sh` **不处理任何参数**
#         ⇒ `bash scripts/deploy-test-gate.sh --help` 会**跑全量闸门**
#           （本机 go 跑 8 包 ≈ 3.6 分钟；无 go 时还会拉起 docker）
#         ⇒ 运维想看用法，却拿到一次完整的部署前测试；
#         ★ **未知参数**同样如此 ⇒ 静默跑全量，连报错都没有。
#   F-22  基线树是 `git worktree add --detach` 建的，**只在 EXIT trap 里移除**
#         ⇒ 被 SIGKILL（OOM / kill -9）打断时 trap 不跑
#         ⇒ `.git/worktrees/<name>` 的**管理记录永久残留** ⇒ 一次一次累积。
#         ★ 而 `git worktree prune` **救不了**：SIGKILL 时 mktemp 目录**也还在**
#           ⇒ prune 的判据是「工作目录已消失」⇒ 它认为这个 worktree 还活着。
#
# 覆盖：
#   AC-1  --help 打印用法并以 0 退出，**且没有跑闸门**。
#   AC-2  -h 与 --help 同形。
#   AC-3  未知参数 ⇒ 退出码 2 + 用法，**且没有跑闸门**。
#   AC-4  DD_SKIP_TEST_GATE=1 时 --help 仍然打印用法（帮助不是闸门）。
#   AC-5  启动时清掉上一次 SIGKILL 留下的 llm-gateway-test-gate.* 残留。
#   AC-6  清扫**不碰**不属于本闸门的 worktree（按名字前缀精确匹配）。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="$REPO_ROOT/scripts/deploy-test-gate.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
die() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }
ok() { PASS=$((PASS + 1)); }

[[ -f "$GATE" ]] || { echo "missing $GATE" >&2; exit 1; }

# run_gate <cwd> <script> <args...> → stdout+stderr 落在 $TMP/out，退出码落在 $RC
#
# ★ 脚本路径必须**显式**传：脚本按**自身位置**推导 REPO_ROOT ⇒ 跑副本时
#   REPO_ROOT 才是那个临时仓库 ⇒ 否则清扫会打在**真仓库**的 .git/worktrees 上
#   （⇒ 测的就不是临时仓库，而是真仓库）。
run_gate() {
  local cwd="$1"; shift
  local script="$1"; shift
  ( cd "$cwd" && bash "$script" "$@" ) >"$TMP/out" 2>&1
  RC=$?
}

# ── AC-1 / AC-2 / AC-3 / AC-4：参数契约 ────────────────────────────────

for flag in --help -h; do
  run_gate "$REPO_ROOT" "$GATE" "$flag"
  if [[ $RC -ne 0 ]]; then
    die "$flag 应以 0 退出，实得 $RC"
  else
    ok
  fi
  grep -q '用法:' "$TMP/out" || die "$flag 应打印用法"
  grep -q -- '--help' "$TMP/out" || die "$flag 应打印选项说明"
  # ★ 关键断言：**没有跑闸门**（闸门会打这一行）。
  if grep -q '要跑的包' "$TMP/out"; then
    die "$flag 不该跑闸门（却打出了闸门进度行）"
  else
    ok
  fi
done

run_gate "$REPO_ROOT" "$GATE" --definitely-not-a-flag
if [[ $RC -eq 0 ]]; then
  die "未知参数应非 0 退出"
else
  ok
fi
grep -q '用法:' "$TMP/out" || die "未知参数应打印用法"
if grep -q '要跑的包' "$TMP/out"; then
  die "未知参数不该跑闸门"
else
  ok
fi

DD_SKIP_TEST_GATE=1 run_gate "$REPO_ROOT" "$GATE" --help
if [[ $RC -ne 0 ]]; then
  die "DD_SKIP_TEST_GATE=1 时 --help 仍应退出 0，实得 $RC"
else
  ok
fi
grep -q '用法:' "$TMP/out" || die "DD_SKIP_TEST_GATE=1 时 --help 仍应打印用法"

# ── AC-5 / AC-6：残留清扫 ──────────────────────────────────────────────
#
# ★ 在**临时 git 仓库**里跑（脚本按自身位置推导 REPO_ROOT ⇒ 必须复制过去）
#   ⇒ 绝不碰真实仓库的 .git/worktrees。
repo="$TMP/repo"
mkdir -p "$repo/scripts"
cp "$GATE" "$repo/scripts/deploy-test-gate.sh"
chmod +x "$repo/scripts/deploy-test-gate.sh"
git -C "$repo" init -q .

stale_dir="$TMP/llm-gateway-test-gate.SIMULATED/base"
mkdir -p "$stale_dir" "$repo/.git/worktrees/stale-gate"
printf 'gitdir: %s\n' "$stale_dir/.git" >"$repo/.git/worktrees/stale-gate/gitdir"

other_dir="$TMP/somebody-elses-worktree"
mkdir -p "$other_dir" "$repo/.git/worktrees/other-owner"
# ★ 夹具必须**真实**：git 记录的 gitdir 指向工作目录里的 .git **文件**；
#   若那个文件不存在，`git worktree prune`（标准清理）会合法地把它删掉
#   ⇒ 那就不是「误伤」，而是 prune 在干它该干的事。
printf 'gitdir: %s\n' "$repo/.git/worktrees/other-owner" >"$other_dir/.git"
printf 'gitdir: %s\n' "$other_dir/.git" >"$repo/.git/worktrees/other-owner/gitdir"

DD_SKIP_TEST_GATE=1 run_gate "$repo" "$repo/scripts/deploy-test-gate.sh"

if [[ -d "$repo/.git/worktrees/stale-gate" ]]; then
  die "本闸门的 SIGKILL 残留没有被清掉"
else
  ok
fi
if [[ -d "$stale_dir" ]]; then
  die "本闸门的 SIGKILL 残留目录没有被清掉"
else
  ok
fi
if [[ ! -d "$repo/.git/worktrees/other-owner" ]]; then
  die "清扫误伤了**不属于本闸门**的 worktree"
else
  ok
fi
if [[ ! -d "$other_dir" ]]; then
  die "清扫误删了**不属于本闸门**的 worktree 目录"
else
  ok
fi

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
