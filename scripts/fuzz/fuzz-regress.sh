#!/bin/bash
# fuzz 失败样本回归流程驱动：
#   1. run     —— 对全部 IR fuzz 目标限时运行；失败时定位最小化后的崩溃样本并做脱敏检查
#   2. install —— 把（已最小化的）崩溃样本经脱敏检查后安装为 testdata 回归语料
#
# Go fuzz 引擎在失败退出前已自动完成样本最小化（minimization），本脚本不再重复最小化，
# 只负责定位、脱敏、入库与回归验证。
#
# 用法:
#   scripts/fuzz/fuzz-regress.sh run [fuzztime]     # fuzztime 默认 5s
#   scripts/fuzz/fuzz-regress.sh install <崩溃样本文件> <Fuzz目标名>
# 环境变量:
#   IR_PACKAGE_DIR   IR 包相对路径（默认 internal/ir）
#   SKIP_SANITIZE=1  跳过脱敏检查（仅限本地临时调试，禁止用于入库）
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IR_PACKAGE_DIR="${IR_PACKAGE_DIR:-internal/ir}"
MODULE_PATH="$(cd "$REPO_ROOT" && go list -m)"
SANITIZER="$REPO_ROOT/scripts/fuzz/sanitize_corpus.sh"

# 仓库内全部 fuzz 目标（新增 fuzz 函数时在此登记）
FUZZ_TARGETS=(
  FuzzIRParsersNeverPanic
  FuzzIRStreamParsersNeverPanic
)

fuzz_cache_dir() {
  local target="$1"
  echo "$(go env GOCACHE)/fuzz/$MODULE_PATH/$IR_PACKAGE_DIR/$target"
}

locate_crashers() {
  local target="$1" dir crasher found=0
  dir="$(fuzz_cache_dir "$target")"
  [ -d "$dir" ] || return 0
  while IFS= read -r crasher; do
    found=1
    echo "$crasher"
  done < <(find "$dir" -type f ! -name '*.lock' 2>/dev/null | sort)
  [ "$found" -eq 1 ]
}

do_run() {
  local fuzztime="${1:-5s}" target log logdir rc
  logdir="$(mktemp -d /tmp/fuzz-regress.XXXXXX)"
  local failed_target=""
  for target in "${FUZZ_TARGETS[@]}"; do
    log="$logdir/$target.log"
    echo "==> fuzz $target (fuzztime=$fuzztime)"
    set +e
    (cd "$REPO_ROOT" && go test "./$IR_PACKAGE_DIR" -run '^$' -fuzz "^$target\$" \
      -fuzztime="$fuzztime" -count=1 >"$log" 2>&1)
    rc=$?
    set -e
    if [ "$rc" -eq 0 ]; then
      echo "    PASS ($(tail -n 1 "$log" | sed 's/^[[:space:]]*//'))"
      continue
    fi

    echo "    FAIL (exit=$rc)，日志: $log"
    grep -E "Failing input|panic|FAIL" "$log" | head -5 | sed 's/^/    /' || true
    failed_target="$target"

    echo ""
    echo "==> 定位最小化崩溃样本"
    local crasher_list=()
    while IFS= read -r c; do crasher_list+=("$c"); done < <(locate_crashers "$target")
    if [ "${#crasher_list[@]}" -gt 0 ]; then
      local c sanitized_all=1
      for c in "${crasher_list[@]}"; do
        echo "    崩溃样本: $c"
        if [ "${SKIP_SANITIZE:-0}" = "1" ]; then
          echo "    [SKIP_SANITIZE=1] 跳过脱敏检查"
        elif bash "$SANITIZER" "$c"; then
          :
        else
          sanitized_all=0
          echo "    ⚠️ 该样本命中敏感串，禁止直接入库；请按上方提示脱敏后重试。"
        fi
      done
      echo ""
      echo "后续步骤（确认崩溃可复现且已脱敏后执行）："
      for c in "${crasher_list[@]}"; do
        echo "  scripts/fuzz/fuzz-regress.sh install '$c' $target"
      done
      [ "$sanitized_all" -eq 1 ] || echo "（存在未通过脱敏检查的样本，install 会被拒绝）"
    else
      echo "    未在 fuzz cache 找到样本文件，请检查日志: $log"
    fi
    echo ""
  done

  if [ -n "$failed_target" ]; then
    echo "结果: fuzz 目标 $failed_target 失败，请按上述流程处置。"
    exit 1
  fi
  echo "结果: 全部 ${#FUZZ_TARGETS[@]} 个 fuzz 目标通过。"
}

do_install() {
  local src="$1" target="$2"
  [ -f "$src" ] || { echo "崩溃样本不存在: $src" >&2; exit 2; }

  local known=0 t
  for t in "${FUZZ_TARGETS[@]}"; do
    [ "$t" = "$target" ] && known=1
  done
  [ "$known" -eq 1 ] || { echo "未知 fuzz 目标: $target（已登记: ${FUZZ_TARGETS[*]}）" >&2; exit 2; }

  if [ "${SKIP_SANITIZE:-0}" != "1" ]; then
    echo "==> 脱敏检查"
    bash "$SANITIZER" "$src"
  else
    echo "[SKIP_SANITIZE=1] 跳过脱敏检查（仅限本地调试）"
  fi

  local dest_dir="$REPO_ROOT/$IR_PACKAGE_DIR/testdata/fuzz/$target"
  mkdir -p "$dest_dir"
  local dest
  dest="$dest_dir/regression-$(date +%Y%m%d)-$(basename "$src")"
  cp "$src" "$dest"
  echo "==> 已安装回归语料: $dest"

  echo "==> 回归验证（普通 go test 会执行 testdata 语料）"
  (cd "$REPO_ROOT" && go test "./$IR_PACKAGE_DIR" -run "^$target\$" -count=1)
  echo "回归验证通过：崩溃样本已固化为永久回归用例。"
}

case "${1:-}" in
  run)      shift; do_run "${1:-5s}" ;;
  install)  shift; [ $# -eq 2 ] || { echo "用法: $0 install <崩溃样本文件> <Fuzz目标名>" >&2; exit 2; }; do_install "$1" "$2" ;;
  *) sed -n '2,20p' "${BASH_SOURCE[0]}"; exit 2 ;;
esac
