#!/usr/bin/env bash
# scripts/check-archive-table-sizes.sh
#
# 检查归档表的实际存储占用，评估删除可释放的空间
#
# Usage:
#   ./scripts/check-archive-table-sizes.sh [184|71|local]

set -euo pipefail

TARGET="${1:-184}"

case "$TARGET" in
  184)
    K8S_NAMESPACE="pms-test"
    K8S_DEPLOY="llm-gateway-pg"
    SSH_HOST="root@14.103.112.184"
    SSH_PORT="25022"
    ;;
  71)
    # 71 服务器配置
    SSH_HOST="root@14.103.174.71"
    SSH_PORT="25022"
    ;;
  local)
    # 本地 Docker 容器
    CONTAINER="llm-gateway-citus"
    ;;
  *)
    echo "Usage: $0 [184|71|local]"
    exit 1
    ;;
esac

PG_DB="llm_gateway"
PG_USER="llm_gateway"

# SQL 查询：获取所有归档表和主表的大小
read -r -d '' QUERY <<'SQL' || true
-- 归档表和主表存储分析
WITH table_sizes AS (
  SELECT 
    schemaname,
    tablename,
    pg_total_relation_size(schemaname||'.'||tablename) as size_bytes,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size_human
  FROM pg_tables
  WHERE schemaname = 'public'
    AND (
      tablename LIKE 'request_logs_archive%'
      OR tablename LIKE 'request_wal_archive%'
      OR tablename = 'request_logs'
      OR tablename = 'request_wal'
      OR tablename LIKE 'request_logs_2026%'
      OR tablename LIKE 'request_wal_2026%'
      OR tablename = 'request_logs_default'
      OR tablename = 'request_wal_default'
    )
),
categorized AS (
  SELECT 
    tablename,
    size_bytes,
    size_human,
    CASE 
      WHEN tablename LIKE '%_archive%' THEN 'archive'
      WHEN tablename LIKE '%_default' THEN 'default_partition'
      WHEN tablename ~ '_\d{4}_\d{2}$' THEN 'monthly_partition'
      ELSE 'parent_table'
    END as category
  FROM table_sizes
)
SELECT 
  category,
  tablename,
  size_human,
  size_bytes
FROM categorized
ORDER BY category, size_bytes DESC;

-- 汇总统计
\echo ''
\echo '=== 汇总统计 ==='
SELECT 
  CASE 
    WHEN tablename LIKE '%_archive%' THEN 'ARCHIVE_TABLES'
    WHEN tablename LIKE '%logs_2026%' OR tablename LIKE '%wal_2026%' THEN 'ACTIVE_PARTITIONS'
    WHEN tablename LIKE '%_default' THEN 'DEFAULT_PARTITIONS'
    ELSE 'PARENT_TABLES'
  END as category,
  COUNT(*) as table_count,
  pg_size_pretty(SUM(pg_total_relation_size(schemaname||'.'||tablename))) as total_size,
  SUM(pg_total_relation_size(schemaname||'.'||tablename)) as total_bytes
FROM pg_tables
WHERE schemaname = 'public'
  AND (
    tablename LIKE 'request_logs_archive%'
    OR tablename LIKE 'request_wal_archive%'
    OR tablename = 'request_logs'
    OR tablename = 'request_wal'
    OR tablename LIKE 'request_logs_2026%'
    OR tablename LIKE 'request_wal_2026%'
    OR tablename LIKE '%_default'
  )
GROUP BY category
ORDER BY total_bytes DESC;
SQL

echo "=== 检查 $TARGET 服务器归档表存储情况 ==="
echo ""

if [[ "$TARGET" == "local" ]]; then
  docker exec "$CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -c "$QUERY"
elif [[ "$TARGET" == "184" ]]; then
  # 通过 kubectl 访问 K8s 集群中的 PG
  POD=$(kubectl get pod -n "$K8S_NAMESPACE" -l app="$K8S_DEPLOY" -o jsonpath='{.items[0].metadata.name}')
  if [[ -z "$POD" ]]; then
    echo "ERROR: 找不到 pod $K8S_DEPLOY 在命名空间 $K8S_NAMESPACE"
    exit 1
  fi
  
  echo "使用 Pod: $POD"
  kubectl exec -n "$K8S_NAMESPACE" "$POD" -c citus -- \
    psql -U "$PG_USER" -d "$PG_DB" -c "$QUERY"
else
  # 通过 SSH 访问
  ssh -p "$SSH_PORT" "$SSH_HOST" "docker exec llm-gateway-postgres psql -U $PG_USER -d $PG_DB -c \"$QUERY\""
fi

echo ""
echo "=== 结论 ==="
echo "如果 ARCHIVE_TABLES 总大小较大（如 > 10GB），建议保留归档策略"
echo "如果 ARCHIVE_TABLES 总大小较小（如 < 1GB），可以考虑删除归档表"
