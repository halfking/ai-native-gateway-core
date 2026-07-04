#!/bin/bash
# Report Default Table Sizes
#
# 用途：报告所有 *_default 表的存储使用情况
# 使用：./scripts/partition/report-default-sizes.sh [--env ENV] [--format FORMAT]
#
# 格式：
#   text    纯文本表格（默认）
#   json    JSON 格式（适合自动化）
#   csv     CSV 格式（适合导入）
#
# 示例：
#   ./scripts/partition/report-default-sizes.sh              # 本地环境，文本格式
#   ./scripts/partition/report-default-sizes.sh --env 71     # 71 环境
#   ./scripts/partition/report-default-sizes.sh --format json  # JSON 格式

set -euo pipefail

# ========================================
# 配置
# ========================================

ENV="${1:-local}"
FORMAT="${2:-text}"

case "$ENV" in
  local)
    PGHOST="${PGHOST:-localhost}"
    PGPORT="${PGPORT:-5432}"
    PGUSER="${PGUSER:-kxuser}"
    PGDATABASE="${PGDATABASE:-llm_gateway}"
    ;;
  71)
    PGHOST="llm.kxpms.cn"
    PGPORT="5432"
    PGUSER="kxuser"
    PGDATABASE="llm_gateway"
    ;;
  184)
    PGHOST="184.kxpms.cn"
    PGPORT="5432"
    PGUSER="kxuser"
    PGDATABASE="llm_gateway"
    ;;
  *)
    echo "错误：未知环境 '$ENV'" >&2
    exit 1
    ;;
esac

export PGHOST PGPORT PGUSER PGDATABASE

# ========================================
# 颜色
# ========================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# ========================================
# 查询 SQL
# ========================================

SQL="
WITH default_tables AS (
  SELECT 
    schemaname,
    tablename,
    pg_total_relation_size(schemaname||'.'||tablename) AS total_bytes,
    pg_relation_size(schemaname||'.'||tablename) AS table_bytes,
    pg_indexes_size(schemaname||'.'||tablename) AS indexes_bytes,
    n_tup_ins AS inserts,
    n_tup_upd AS updates,
    n_tup_del AS deletes,
    n_live_tup AS live_rows,
    n_dead_tup AS dead_rows,
    last_vacuum,
    last_autovacuum,
    last_analyze
  FROM pg_stat_user_tables
  WHERE tablename LIKE '%_default'
),
size_warnings AS (
  SELECT 
    tablename,
    total_bytes,
    CASE
      WHEN total_bytes > 10737418240 THEN 'CRITICAL'  -- 10GB
      WHEN total_bytes > 5368709120 THEN 'WARNING'     -- 5GB
      ELSE 'OK'
    END AS status
  FROM default_tables
)
SELECT 
  dt.schemaname,
  dt.tablename,
  dt.total_bytes,
  dt.table_bytes,
  dt.indexes_bytes,
  dt.inserts,
  dt.updates,
  dt.deletes,
  dt.live_rows,
  dt.dead_rows,
  dt.last_vacuum,
  dt.last_autovacuum,
  dt.last_analyze,
  sw.status
FROM default_tables dt
LEFT JOIN size_warnings sw ON dt.tablename = sw.tablename
ORDER BY dt.total_bytes DESC;
"

# ========================================
# 格式化输出
# ========================================

case "$FORMAT" in
  text)
    echo "=== *_default 表存储使用报告 ==="
    echo "环境: $ENV ($PGHOST:$PGPORT/$PGDATABASE)"
    echo "时间: $(date)"
    echo ""
    
    psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -x -c "$SQL" 2>/dev/null || {
      echo "错误：无法连接数据库" >&2
      exit 1
    }
    ;;
    
  json)
    psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "$SQL" 2>/dev/null | \
      awk 'BEGIN { print "[" }
        { 
          gsub(/[|]/, "", $0)
          split($0, a, "|")
          for(i in a) {
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", a[i])
          }
        }
        NR>1 { print "," }
        { print "  {" }
        print "    \"table\": \"" a[2] "\","
        print "    \"total_bytes\": " a[3] ","
        print "    \"table_bytes\": " a[4] ","
        print "    \"indexes_bytes\": " a[5] ","
        print "    \"inserts\": " a[6] ","
        print "    \"updates\": " a[7] ","
        print "    \"deletes\": " a[8] ","
        print "    \"live_rows\": " a[9] ","
        print "    \"dead_rows\": " a[10] ","
        print "    \"status\": \"" a[14] "\""
        print "  }"
        }
      END { print "]" }' 2>/dev/null || {
      echo '{"error": "Failed to connect or query database"}' >&2
      exit 1
    }
    ;;
    
  csv)
    echo "schema,table,total_bytes,total_human,table_bytes,indexes_bytes,inserts,updates,deletes,live_rows,dead_rows,status"
    psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "$SQL" 2>/dev/null | \
      while IFS='|' read -ra parts; do
        # Simple CSV conversion
        echo "${parts[*]}" | sed 's/|/,/g'
      done
    ;;
    
  *)
    echo "错误：未知格式 '$FORMAT'" >&2
    exit 1
    ;;
esac

# ========================================
# 告警检查（仅文本模式）
# ========================================

if [[ "$FORMAT" == "text" ]]; then
  echo ""
  echo "=== 告警检查 ==="
  
  CRITICAL_COUNT=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT COUNT(*) FROM pg_stat_user_tables
    WHERE tablename LIKE '%_default'
      AND pg_total_relation_size(schemaname||'.'||tablename) > 10737418240
  " 2>/dev/null | tr -d ' ')
  
  WARNING_COUNT=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT COUNT(*) FROM pg_stat_user_tables
    WHERE tablename LIKE '%_default'
      AND pg_total_relation_size(schemaname||'.'||tablename) > 5368709120
      AND pg_total_relation_size(schemaname||'.'||tablename) <= 10737418240
  " 2>/dev/null | tr -d ' ')
  
  if [[ "$CRITICAL_COUNT" -gt 0 ]]; then
    echo -e "${RED}🚨 CRITICAL: $CRITICAL_COUNT 个表超过 10GB${NC}"
    echo "  立即执行: ./scripts/partition/manual-promote-default.sh --all"
  elif [[ "$WARNING_COUNT" -gt 0 ]]; then
    echo -e "${YELLOW}⚠️  WARNING: $WARNING_COUNT 个表超过 5GB${NC}"
    echo "  建议执行: ./scripts/partition/manual-promote-default.sh --all --retention 7"
  else
    echo -e "${GREEN}✅ 所有 *_default 表大小正常${NC}"
  fi
fi
