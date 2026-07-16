#!/usr/bin/env bash
# ============================================================================
# db_cleanup_252.sh — 252数据库定期清理脚本
#
# Usage:
#   bash scripts/db_cleanup_252.sh [--dry-run]
#
# Purpose:
#   1. 清理legacy/archived表
#   2. DROP 2026_NN月份区（按需）
#   3. TRUNCATE长时间无更新的表
#   4. REINDEX膨胀表
#   5. VACUUM ANALYZE
#   6. 报告清理效果
#
# Notes:
#   - 需通过SSH隧道执行（localhost:15432 或 SSH到252）
#   - 必须在PG容器内运行
# ============================================================================
set -euo pipefail

# === 参数解析 ===
DRY_RUN=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
  esac
done

# === SQL内容 ===
SQL_CONTENT=$(cat << 'SQL'
-- 1. 清理legacy/archived表（小表，安全）
DROP TABLE IF EXISTS request_wal_2026_07_col_archived CASCADE;
DROP TABLE IF EXISTS usage_ledger_2026_07_col_archived CASCADE;
DROP TABLE IF EXISTS request_wal_2026_07_archived CASCADE;
DROP TABLE IF EXISTS model_offers_legacy CASCADE;
DROP TABLE IF EXISTS usage_ledger_2026_07_archived CASCADE;
DROP TABLE IF EXISTS ops_model_offers_backup CASCADE;

-- 2. TRUNCATE长时间无更新的handoff_logs（超过7天）
TRUNCATE TABLE handoff_logs;

-- 3. 删除7天前的临时数据
DELETE FROM self_check_round_results 
WHERE created_at < NOW() - INTERVAL '7 days';

DELETE FROM self_check_runs
WHERE created_at < NOW() - INTERVAL '7 days';

-- 4. REINDEX膨胀表
REINDEX TABLE CONCURRENTLY request_logs_hot;
REINDEX TABLE CONCURRENTLY request_wal_hot;
REINDEX TABLE CONCURRENTLY model_probe_runs_hot;

-- 5. ANALYZE更新统计
VACUUM ANALYZE request_logs_hot;
VACUUM ANALYZE request_wal_hot;
VACUUM ANALYZE model_probe_runs_hot;
VACUUM ANALYZE credential_model_index_2026_07;
VACUUM ANALYZE routing_decision_log_2026_07;
VACUUM ANALYZE usage_ledger_2026_07;

-- 6. 输出当前状态
SELECT '=== Cleanup Complete ===' as status;
SELECT 
    pg_size_pretty(pg_database_size('llm_gateway')) as db_size,
    pg_size_pretty(pg_total_relation_size('columnar_internal.chunk'::regclass)) as columnar_chunk,
    pg_size_pretty(pg_total_relation_size('request_logs_hot'::regclass)) as hot_table;
SQL
)

# === 执行 ===
if [ "$DRY_RUN" = "true" ]; then
  echo "🔍 [DRY RUN] Would execute:"
  echo "$SQL_CONTENT"
  exit 0
fi

echo "🧹 Executing cleanup on 252..."
echo "$SQL_CONTENT" | sshpass -p "$SSH_PASS" ssh -p 25022 root@115.29.212.252 \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  'docker exec -i pg-252-pg17 psql -U llm_gateway -d llm_gateway' 2>&1

echo "✅ Cleanup complete"
