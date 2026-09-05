#!/usr/bin/env bash
# scripts/compression-benchmark.sh — 245 生产日志压缩效果 benchmark 入口
#
# 数据依赖：需要能访问 245 生产 request_logs 的 PostgreSQL DSN（联系运维获取，注意脱敏）。
# 计算依赖：本仓库已内置真实 harness `cmd/compression-bench`（见其内部 processRow）。
#
# 本脚本是对 `cmd/compression-bench` 的薄封装。默认只读分析且不写本地报告；
# 传入 --output 后才会写 aggregate-only JSON。
#
# 关于"算法选择对比"的边界（重要）：
#   cmd/compression-bench 当前通过 SessionCompressor.Prepare 跑 "v4 intelligent" 全链路，
#   还不直接吃 LLM_GATEWAY_COMPRESSION_SELECTOR=adaptive。要对比
#   intelligent vs adaptive（GW-10 Phase 2 的 AdaptiveSelector）的信息密度 / 遗失率，
#   需先把 strategy.AdaptiveSelector 接进 bench harness 的 processRow（后续工作，
#   见 docs/design/compression-strategy-selector.md §12.6 待办 #2）。
#   本脚本先把"单一策略 × 真实流量"的基线跑出来，作为后续对比的 baseline。
#
# 用法：
#   DATABASE_URL='postgres://…' ./scripts/compression-benchmark.sh \
#     --days 7 --max-samples 1000 --protocol openai --context-window 128000

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DSN="${DATABASE_URL:-}"
DAYS=7
MAX_SAMPLES=1000
PROTOCOL="openai"
CONTEXT_WINDOW=128000
OUTPUT=""
SKIP_LLM_SUMMARY=""
SHARE_SESSION=""
SERIAL=""

usage() {
  cat <<EOF
Usage: $0 [OPTIONS]

Options:
  --days N           Lookback window in days (default: 7)
  --max-samples N    Max rows to process (default: 1000)
  --protocol P       openai | anthropic-messages (default: openai)
  --context-window N Model context window in tokens (default: 128000)
  --output PATH      Write results JSON here
  --skip-llm-summary Skip LLM-summary path (mechanical-only, faster)
  --share-session    Collapse all rows into one session (delta-append test)
  --serial           Serial row processing (required with --share-session)
  --help             Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --days) DAYS="$2"; shift 2 ;;
    --max-samples) MAX_SAMPLES="$2"; shift 2 ;;
    --protocol) PROTOCOL="$2"; shift 2 ;;
    --context-window) CONTEXT_WINDOW="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --skip-llm-summary) SKIP_LLM_SUMMARY="--skip-llm-summary"; shift ;;
    --share-session) SHARE_SESSION="--share-session"; shift ;;
    --serial) SERIAL="--serial"; shift ;;
    --help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ -z "$DSN" ]]; then
  echo "Error: DATABASE_URL is required (inject it through the approved secret channel)" >&2
  usage
  exit 1
fi

if [[ -z "$(command -v go)" ]]; then
  echo "Error: go not found in PATH" >&2
  exit 1
fi

echo "== compression benchmark (read-only, no artifact by default) =="
echo "  days=$DAYS max-samples=$MAX_SAMPLES protocol=$PROTOCOL window=$CONTEXT_WINDOW"
if [[ -n "$OUTPUT" ]]; then
  echo "  aggregate output=$OUTPUT"
fi
echo

cd "$PROJECT_ROOT"
args=(
  --days "$DAYS"
  --max-samples "$MAX_SAMPLES"
  --protocol "$PROTOCOL"
  --context-window "$CONTEXT_WINDOW"
)
if [[ -n "$OUTPUT" ]]; then
  args+=(--output "$OUTPUT")
fi
if [[ -n "$SKIP_LLM_SUMMARY" ]]; then
  args+=($SKIP_LLM_SUMMARY)
fi
if [[ -n "$SHARE_SESSION" ]]; then
  args+=($SHARE_SESSION)
fi
if [[ -n "$SERIAL" ]]; then
  args+=($SERIAL)
fi
exec go run ./cmd/compression-bench "${args[@]}"
