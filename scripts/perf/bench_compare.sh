#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# 核心包 benchmark 基线管理入口：
#   --update  运行 bench-core，将结果固化为 docs/perf/bench-baseline.txt（需人工提交）
#   --check   运行 bench-core，对照基线检查回归（超过阈值退出 1）
# 每次运行的原始输出都会归档到 docs/perf/bench-history/。
# 环境变量：
#   BENCH_COUNT  benchmark 次数（默认 1；加大后取 ns/op 最小值，降噪）
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASELINE="$REPO_ROOT/docs/perf/bench-baseline.txt"
HISTORY_DIR="$REPO_ROOT/docs/perf/bench-history"
THRESHOLDS="$REPO_ROOT/scripts/perf/bench_thresholds.txt"
BENCH_COUNT="${BENCH_COUNT:-1}"

MODE="${1:-}"
case "$MODE" in
  --update|--check) ;;
  *) echo "用法: $0 --update | --check" >&2; exit 2 ;;
esac

RAW="$HISTORY_DIR/bench-$(date +%Y%m%d-%H%M%S).txt"
mkdir -p "$HISTORY_DIR"

echo "==> 运行核心 benchmark（count=${BENCH_COUNT}），原始输出: $RAW"
(cd "$REPO_ROOT" && go test ./internal/ir ./domains/dispatch ./domains/streaming/... \
  -run '^$' -bench . -benchmem -count="$BENCH_COUNT" -timeout=900s | tee "$RAW")

echo ""
if [ "$MODE" = "--update" ]; then
  python3 "$REPO_ROOT/scripts/perf/bench_compare.py" update \
    --baseline "$BASELINE" --input "$RAW" --note "make bench-baseline"
else
  python3 "$REPO_ROOT/scripts/perf/bench_compare.py" check \
    --baseline "$BASELINE" --input "$RAW" --thresholds "$THRESHOLDS"
fi
