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
#   AC-7  嵌套模块分箱：包就是嵌套模块根目录本身时记成 `./`（2026-11-26：旧实现记成
#         重复路径 ⇒ go list 取不到包清单 ⇒ 闸门未得出结论 ⇒ 干净树部署被挡）。
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

# ── AC-7：嵌套模块分箱（★ 2026-11-26 L123 轮，211 部署实测缺陷）────────────
#
# ★ 缺陷形态：被选中的包**就是嵌套模块根目录本身**（./plugin-runtime/config）时，
#   旧实现把它记成 `./plugin-runtime/config` ⇒ 到模块根下再列一次 ⇒ 路径重复
#   一层 ⇒ `go list` 取不到包清单 ⇒ 闸门「未得出结论」⇒ 干净树部署被挡。
# ★ 判据已搬进 scripts/lib/gate-nested-module-binning.sh ⇒ 这里**直接实跑**它。
# ★ 需要 bash ≥ 4.2（关联数组 + declare -gA）；低版本如实跳过、不计入通过数。
BINNING_LIB="$REPO_ROOT/scripts/lib/gate-nested-module-binning.sh"
if (( BASH_VERSINFO[0] < 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 2) )); then
  echo "SKIP AC-7: 嵌套模块分箱需要 bash >= 4.2（当前 ${BASH_VERSION}）"
elif [[ ! -f "$BINNING_LIB" ]]; then
  die "AC-7: 缺 $BINNING_LIB"
else
  binrepo="$TMP/binrepo"
  mkdir -p "$binrepo/plugin-runtime/config" "$binrepo/installer/internal/dbinit" \
           "$binrepo/admin" "$binrepo/bg" "$binrepo/cmd/gateway/foo"
  : >"$binrepo/go.mod"
  : >"$binrepo/plugin-runtime/config/go.mod"
  : >"$binrepo/installer/go.mod"
  bin_out="$( "$BASH" -c '
    set -u
    . "$1"
    gate_bin_packages_by_module "$2" ./admin ./plugin-runtime/config ./installer/internal/dbinit ./bg ./cmd/gateway/foo
    printf "MAIN=%s\n" "${GATE_MAIN_PACKAGES[*]}"
    printf "MODULES=%s\n" "${GATE_NESTED_MODULES[*]}"
    for m in "${GATE_NESTED_MODULES[@]}"; do printf "PKGS[%s]=[%s]\n" "$m" "${GATE_NESTED_PKGS[$m]}"; done
  ' _ "$BINNING_LIB" "$binrepo" 2>"$TMP/bin-err" )"
  if [[ -s "$TMP/bin-err" ]]; then
    die "AC-7: 分箱函数有 stderr 输出：$(head -2 "$TMP/bin-err")"
  fi
  # ★ 模块根自身的包记成 `./`（不是重复路径）；子目录的包是模块内相对路径；模块列表按首次出现排序。
  want_main="MAIN=./admin ./bg ./cmd/gateway/foo"
  want_modules="MODULES=plugin-runtime/config installer"
  want_pkgs1="PKGS[plugin-runtime/config]=[ ./]"
  want_pkgs2="PKGS[installer]=[ ./internal/dbinit]"
  for want in "$want_main" "$want_modules" "$want_pkgs1" "$want_pkgs2"; do
    if grep -qxF -- "$want" <<<"$bin_out"; then
      ok
    else
      die "AC-7: 分箱结果缺行：${want}；实际：$(tr '\n' '|' <<<"$bin_out")"
    fi
  done
  # ★ 只传主模块的包 ⇒ 嵌套列表为空（调用方据此跳过嵌套箱）。
  only_main="$( "$BASH" -c '
    set -u
    . "$1"
    gate_bin_packages_by_module "$2" ./admin
    printf "N=%s MAIN=%s\n" "${#GATE_NESTED_MODULES[@]}" "${GATE_MAIN_PACKAGES[*]}"
  ' _ "$BINNING_LIB" "$binrepo" 2>&1 )"
  if [[ "$only_main" == "N=0 MAIN=./admin" ]]; then
    ok
  else
    die "AC-7: 只有主模块包时嵌套列表应为空，实际：$only_main"
  fi
fi

echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
