#!/usr/bin/env bash
# deploy_timeout_portability_test.sh —— 部署脚本的 timeout 可移植性回归门。
#
# ## 它守的是什么
#
# 2026-10-07 修掉的缺陷（commit df69ef0be）：scripts/deploy-local-lib.sh 的
# dl_detect_resources 直接写 `timeout 5 docker info` / `timeout 3 docker compose
# version`。`timeout` 是 coreutils 的可执行文件，**stock macOS 默认没有**（装了
# coreutils 之后命令名还叫 gtimeout）。于是 macOS 上该命令以 127 失败 →
# DL_DOCKER 恒为 0 → **整块 docker 探测被静默跳过**：即使 Docker Desktop 正在
# 运行也不会走 docker 路径，且没有任何报错。
#
# 讽刺的是那段守卫的注释（2026-09-18）写明它正是给「macOS Docker Desktop 繁忙时
# docker info 偶发卡住」加的——防护为 macOS 而加，却在 macOS 上把功能整个关掉。
#
# ## 为什么单独一个文件，而不是并进 deploy_local_contract_test.sh
#
# 那套契约测试依赖共享部署库 SSOT（AIAN_DEPLOY_LIB 默认解析到
# $HOME/workspace/ai-native-tools/deploy-lib）。CI runner 上没有这个目录，整个套件
# 会在 line 165 的 fixture 组装处 `cd` 失败并 exit 64——**接进 verify.sh 会直接把
# 主门跑挂**。本文件只做两件事：静态断言没有裸 timeout，以及行为断言 _dl_timeout
# 真的会超时。零外部依赖，因此在任何 runner（含 macOS）都能跑。
#
# 行为断言是这道门的重点：光 grep `timeout` 挡不住「把裸调用换成另一个不存在
# 的二进制」这类同形退化，_dl_timeout 的三级回退必须真的在**本机没有 timeout /
# gtimeout** 时也能限时成功。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT/scripts/deploy-local-lib.sh"
# 同样使用裸 timeout 的两个测试脚本（deploy-local-frontend_test.sh 此前完全不
# source 该 lib，preflight 的 helper 只在内层 bash -c 里拿得到）。
GUARDED=(
  "$ROOT/scripts/deploy-local-preflight_test.sh"
  "$ROOT/scripts/deploy-local-frontend_test.sh"
)

pass() { printf 'PASS: %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# 1. lib 必须真的定义 _dl_timeout，否则下面所有行为断言都无从谈起。
grep -q '^_dl_timeout() {' "$LIB" \
  || fail "$LIB 未定义 _dl_timeout() —— docker 探测又会回到裸 timeout 上"

# 2. 静态门：这些文件里不得再出现裸 `timeout <秒数>` 调用。
#    排除注释与 _dl_timeout 自身的实现行（它们要谈 timeout 这个词）。
for f in "$LIB" "${GUARDED[@]}"; do
  [[ -f "$f" ]] || fail "被守护文件不存在: $f"
  hits=$(grep -nE '(^|[;&|(]|[[:space:]])timeout[[:space:]]+[0-9]+' "$f" \
         | grep -vE '^[0-9]+:[[:space:]]*#' \
         | grep -vE '_dl_timeout[[:space:]]+[0-9]+' || true)
  [[ -z "$hits" ]] || fail "$f 仍有裸 timeout 调用（stock macOS 无 coreutils 会 127）：
$hits"
done

# 3. 静态门：docker 探测的两个调用点必须走 _dl_timeout。
grep -q '_dl_timeout 5 docker info' "$LIB" \
  || fail "dl_detect_resources 的 docker info 探测未改用 _dl_timeout"
grep -q '_dl_timeout 3 docker compose version' "$LIB" \
  || fail "dl_detect_resources 的 docker compose 探测未改用 _dl_timeout"

pass 'no bare GNU timeout invocations remain in the deploy scripts'

# ---------------------------------------------------------------------------
# 4. 行为门：_dl_timeout 必须在**本机确实没有 timeout / gtimeout** 时仍然限时。
#    若 runner 装了 coreutils 就跳过这一段——那种情况下走的是第一级回退，
#    纯 bash 那级未被覆盖，而 CI（ubuntu）恒有 timeout，靠它覆盖不到。
# ---------------------------------------------------------------------------
if command -v timeout >/dev/null 2>&1 || command -v gtimeout >/dev/null 2>&1; then
  pass 'SKIP pure-bash watchdog assertions (timeout/gtimeout present on this host)'
else
  # shellcheck disable=SC1090
  source "$LIB"

  rc=0; _dl_timeout 5 sh -c 'exit 0' || rc=$?
  [[ "$rc" -eq 0 ]] || fail "_dl_timeout 快速成功应返回 0，实际 $rc"

  rc=0; _dl_timeout 5 sh -c 'exit 7' || rc=$?
  [[ "$rc" -eq 7 ]] || fail "_dl_timeout 必须透传真实退出码 7，实际 $rc"

  start=$SECONDS
  rc=0; _dl_timeout 1 sleep 30 || rc=$?
  elapsed=$(( SECONDS - start ))
  [[ "$rc" -eq 124 ]] || fail "_dl_timeout 超时应返回 124，实际 $rc"
  [[ "$elapsed" -le 5 ]] || fail "_dl_timeout 1s 上限耗时 ${elapsed}s，看门狗没生效"

  rc=0; out=$( _dl_timeout 5 sh -c 'echo inner-out' ) || rc=$?
  [[ "$rc" -eq 0 && "$out" == 'inner-out' ]] \
    || fail "_dl_timeout 未正确透传 stdout（rc=$rc out=[$out]）"

  pass 'pure-bash watchdog honours the cap, exit codes and stdout'
fi

echo 'OK: deploy timeout portability contract'