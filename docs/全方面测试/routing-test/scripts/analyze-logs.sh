#!/usr/bin/env bash
# 基于网关已存在的 JSON slog 事件生成摘要。
# 接受 docker logs 导出或 LLM_GATEWAY_LOG_FILE 产生的文件；不会读取数据库。

set -euo pipefail

LOG_FILE="${1:?usage: analyze-logs.sh <gateway-json-log-file>}"
OUTPUT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/results"
mkdir -p "$OUTPUT_DIR"
REPORT="$OUTPUT_DIR/routing-analysis-$(date +%Y%m%d-%H%M%S).md"
EVENTS_FILE="$(mktemp)"
trap 'rm -f "$EVENTS_FILE"' EXIT

if [[ ! -r "$LOG_FILE" ]]; then
    printf 'Cannot read log file: %s\n' "$LOG_FILE" >&2
    exit 1
fi
if ! command -v jq >/dev/null; then
    printf 'jq is required to analyze JSON slog output.\n' >&2
    exit 1
fi

# docker logs can contain non-JSON startup lines. Retain only valid slog records.
jq -R 'fromjson? | select(.)' "$LOG_FILE" > "$EVENTS_FILE"

{
    printf '# 路由测试日志分析\n\n'
    printf '来源：`%s`\n\n' "$LOG_FILE"
    printf '## 上游尝试\n\n```text\n'
    jq -s -r '
        [.[] | select(.msg == "upstream_http_attempt")] as $attempts
        | if ($attempts | length) == 0 then
            "No upstream_http_attempt events found."
          else
            ($attempts | sort_by(.latency_ms)) as $ordered
            | "attempts=\($attempts | length) avg_latency_ms=\(($attempts | map(.latency_ms) | add / length) | floor) p50_ms=\($ordered[((length - 1) * 0.50 | floor)].latency_ms) p95_ms=\($ordered[((length - 1) * 0.95 | floor)].latency_ms) max_ms=\($ordered[-1].latency_ms)",
              ($attempts | sort_by(.credential_id) | group_by(.credential_id)[] | "credential=\(.[0].credential_id) attempts=\(length)"),
              ($attempts | map(select((.upstream_status // 0) >= 500 or (.err_kind // "") != "")) | sort_by([.err_kind, .upstream_status]) | group_by([.err_kind, .upstream_status])[]? | "failure=\(.[0].err_kind // "none"):\(.[0].upstream_status // 0) count=\(length)")
          end
    ' "$EVENTS_FILE"
    printf '```\n\n'
    printf '## 重试与无候选恢复\n\n```json\n'
    jq -c 'select(.msg == "goal_retry_attempt" or .msg == "goal_retry_timeout" or .msg == "executor: no candidates after router" or .msg == "router: all candidates unavailable" or .msg == "router: degraded mode activated")' "$EVENTS_FILE" || true
    printf '```\n\n'
    printf '## 限制\n\n'
    printf '此报告统计网关已经记录的 `upstream_http_attempt`、重试与无候选事件。当前实现没有统一的“入口→路由→协议转换→客户端写回”逐阶段耗时事件，因此 S24 只能得到上游尝试时延；新增阶段阈值前需要先为网关埋点并添加单元/集成测试。\n'
} > "$REPORT"

printf 'Report: %s\n' "$REPORT"
