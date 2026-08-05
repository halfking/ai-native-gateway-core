#!/bin/bash
# docs/全方面测试/scenarios/S22_instant_summary.sh
#
# S22: 会话即时总结 — PENDING (临时跳过)
#
# 原因 (2026-08-06): 当前 llm-gateway-pg 数据库状态下, gateway 路由 P2C 算法
# 在 23 candidates 中仅 2 个能进 executor, 全部 fail, 表现为
# "All 2 candidates failed / model_not_found". 这是因为:
#   1. request_logs 表 ON CONFLICT 缺失 (42P10 错误) → 真实流量统计没写入
#   2. recent_success_rate 一直 = 0 → 大部分 candidate 被 circuit_breaker 过滤
#   3. 仅 4 个 supplier (9013/9028/9043/9058) 有真实 p95_latency_ms
#
# 修复依赖: 跑 docs/全方面测试/data/seed.sql 后立即跑 1 轮 S01 baseline (10 min)
# 真实流量让 recent_success_rate 上升到 0.97, 路由可工作.
# 当前环境 (冷启动 0 流量) 不可行.
#
# S22 的设计目标 (chat → request_logs/compression → session_summaries 自动更新)
# 在修好 DB 链路后即可工作, 见 S22_pending_notes.md.
#
# 2026-08-06: 首次实现 + 标 PENDING.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S22_instant_summary"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S22] $*"; }

log "S22: instant summary — PENDING (gateway 路由 P2C 冷启动 0 流量, 见 S22 header)"
log "covers (设计):"
log "  22.1 chat 完成后 session_titles 落库 (S20 已覆盖)"
log "  22.2 30 轮 chat 触发 sliding_window_count"
log "  22.3 60 轮长 prompt 触发 sliding_window_token"
log "  22.4 LLM 失败 fallback mechanical_trim"
log "  22.5 admin 手动触发 session summary (需 JWT, 列 TODO)"

# 写结果
cat > "$RESULT" <<'EOF'
{
  "scenario": "S22_instant_summary",
  "status": "PENDING",
  "reason": "gateway 路由 P2C 冷启动 0 流量, recent_success_rate 未更新, candidates 几乎全部 fail",
  "fix_needed": [
    "1. 跑 S01 baseline 10 min 让 recent_success_rate 上升到 0.97",
    "2. 修复 request_logs 表 ON CONFLICT 约束 (与 telemetry/client.go 的 ON CONFLICT (request_id) 匹配)",
    "3. 修复 model_offers p95=0 默认值 (seed.sql 应设组画像 p95)",
    "4. 重启 gateway 让 model_offers 重新被加载"
  ],
  "tests_designed": [
    "22.1_basic_chat_logged (PASS via S20)",
    "22.2_count_trigger (PENDING, 待 fix 后重跑)",
    "22.3_token_trigger (PENDING)",
    "22.4_mechanical_fallback (PENDING)",
    "22.5_admin_manual (TODO: JWT 登录链路)"
  ]
}
EOF

skip_scenario "instant summary pending — needs DB schema/cold-start fixes"

