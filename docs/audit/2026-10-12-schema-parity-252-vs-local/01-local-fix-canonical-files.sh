#!/usr/bin/env bash
# 01 — 本地侧修正（canonical 文件投递腿）
# 对账文档：同目录 README.md §4。逐文件均为仓库 canonical 迁移原样执行
# （TSV✓=installer 新装序列已背书 / chan✓=通道重放背书 / 已人工检视）。
# 用法：bash 01-local-fix-canonical-files.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DB="${LLM_GATEWAY_DATABASE_URL:-postgresql://huangxt@127.0.0.1:54318/llm_gateway?sslmode=disable}"
FILES=(
  sql/migrations/startup/351_session_analytics_tables.sql        # TSV✓ 6 张 session 分析表
  sql/migrations/startup/426_task_type_centroids.sql             # TSV✓
  sql/migrations/startup/470_cache_metrics.sql                   # TSV✓ cache_metrics 父表（本地日志 42P01×2）
  sql/migrations/startup/472_cache_metrics_partitions.sql        # TSV✓
  sql/migrations/startup/520_durable_task_settlement_intents.sql # TSV✓
  sql/migrations/startup/527_handoff_durable_goal_state.sql      # TSV✓ 列+状态扩宽+restore 索引
  sql/migrations/startup/541_candidate_binding_scope_revision.sql # TSV✓ chan✓
  sql/migrations/startup/554_goal_runs.sql                       # TSV✓ goal_runs 3 表
  sql/migrations/startup/567_session_analysis_metadata.sql       # TSV✓
  sql/migrations/startup/569_candidate_binding_scope_revision_canonical.sql # TSV✓ chan✓（本地 42P01×219 根修）
  sql/migrations/startup/571_candidate_binding_scope_revision_canonical_priority_hash.sql # TSV✓ chan✓
  sql/migrations/startup/578_candidate_binding_scope_filter_disabled.sql    # chan✓
  sql/migrations/startup/629_audit_attachments_cleanup.sql       # TSV✓
  sql/migrations/startup/632_audit_attachments_filesystem_cleanup.sql # TSV✓
  sql/migrations/startup/819_request_abandoned.sql               # 已检视：CREATE TABLE IF NOT EXISTS
  sql/migrations/local/641_local_shared_platform_schema_fixup.sql # 本地共享平台 fixup（additive-only）
)
# 逐文件容错：单文件失败不中断整腿（520 的 RLS policy 无重放守卫，重放必炸——
# 已在 2026-10-12 首跑实证其效果在库，失败即幂等 no-op）；结尾汇总 FAILED 清单。
command -v psql >/dev/null || { echo "psql not on PATH（本机：export PATH=\"$HOME/kaixuan/pg18/bin:$PATH\"）" >&2; exit 2; }
FAILED=()
for f in "${FILES[@]}"; do
  echo "== applying $f"
  if ! psql -X -v ON_ERROR_STOP=1 -q -f "$ROOT/$f" "$DB"; then
    echo "   !! failed (effects may already be present — verify per README §7)"
    FAILED+=("$f")
  fi
done
if ((${#FAILED[@]})); then
  echo "canonical-files leg done WITH FAILURES:"
  printf '  %s\n' "${FAILED[@]}"
else
  echo "canonical-files leg done clean"
fi
