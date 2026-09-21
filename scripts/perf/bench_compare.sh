#!/bin/bash
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
