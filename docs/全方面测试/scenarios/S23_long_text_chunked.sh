#!/bin/bash
# docs/全方面测试/scenarios/S23_long_text_chunked.sh
#
# S23: 长文本分段式输入与总结输出 — PENDING (与 S22 同原因)
#
# 覆盖 (设计):
#   23.1 60 轮长 prompt (3000 chars/轮) 触发 sliding_window_token
#   23.2 80 轮短 prompt 触发 sliding_window_count
#   23.3 验证 CutMarker IncrementalBuild — 第二轮请求的 outbound_body 含
#         [smm_v1:xxx] marker, summary_marker 出现在 compression_meta
#   23.4 跨协议分段 (OpenAI + Anthropic)
#   23.5 长文本 > 200K tokens (走 tail-trim 路径, 无真 map-reduce)
#
# 真 map-reduce 分段摘要 (LangChain map_reduce_chain 风格) **当前未实现**,
# 当前仅走 trimTextToTokenBudget(900_000) 尾部截断 (domains/hooks/compression/compaction.go:773).
# 待实现 chunked_summarizer.go (新文件) + settings/spec_compression.go 加
# chunk_size_tokens / chunk_overlap_tokens 后, 本测试可扩展为
# 验证 [CHUNK_n]...[MERGE]... 协议.
#
# 2026-08-06: 首次实现 (placeholder, 与 S22 同因).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S23_long_text_chunked"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S23] $*"; }

log "S23: long text chunked — PENDING (gateway 路由 P2C 冷启动 0 流量, 同 S22)"
log "covers (设计):"
log "  23.1 60 轮长 prompt 触发 sliding_window_token"
log "  23.2 80 轮短 prompt 触发 sliding_window_count"
log "  23.3 CutMarker IncrementalBuild (smm_v1 marker)"
log "  23.4 跨协议分段 (OpenAI + Anthropic)"
log "  23.5 真 map-reduce 分段 (TODO: 待 chunked_summarizer.go 实现)"

# 写结果
cat > "$RESULT" <<'EOF'
{
  "scenario": "S23_long_text_chunked",
  "status": "PENDING",
  "reason": "同 S22 — gateway 路由 P2C 冷启动 0 流量, candidates fail",
  "additional_notes": "真 map-reduce chunked summary 本身未实现 (需新代码: domains/hooks/compression/chunked_summarizer.go + settings/spec_compression.go 加 chunk_size_tokens / chunk_overlap_tokens)",
  "tests_designed": [
    "23.1_token_trigger (PENDING)",
    "23.2_count_trigger (PENDING)",
    "23.3_cut_marker_incremental (PENDING)",
    "23.4_cross_protocol (PENDING)",
    "23.5_map_reduce_chunked (TODO: feature missing)"
  ]
}
EOF

skip_scenario "long text chunked pending — needs DB cold-start fix + chunked_summarizer.go"
