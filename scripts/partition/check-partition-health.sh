#!/bin/bash
# ========================================
# 修订历史 / Revision History
# ========================================
# | 版本 | 日期       | 变更                                                          | 作者                |
# |------|------------|---------------------------------------------------------------|---------------------|
# | 1.0  | 2026-07-05 | 初始版本                                                      | Infrastructure Team |
# | 1.1  | 2026-07-05 | 合并 report-default-sizes.sh（新增 --report-only / --format）| Infrastructure Team |
# ========================================
#
# Partition Health Checker
#
# 用途：快速诊断所有分区表的健康状态
# 使用：
#   ./scripts/partition/check-partition-health.sh [--env local|71|184]
#   ./scripts/partition/check-partition-health.sh --report-only [--env X] [--format text|json|csv]
#
# 输出（默认 - 完整诊断 6 节）：
#   1. DEFAULT 表大小和写入统计
#   2. 分区附加状态（ATTACHED vs DETACHED）
#   3. 当月分区状态（2026_07 DETACHED）
#   4. 最近写入活动
#   5. Promote 函数执行状态
#   6. 磁盘空间检查
#
# 输出（--report-only）：
#   仅 DEFAULT 表大小报告（text/json/csv 三种格式）
#   等同于旧 scripts/partition/report-default-sizes.sh
#
# 依赖：psql, PostgreSQL 客户端, bc

set -euo pipefail

# ========================================
# 配置与参数解析
# ========================================

ENV=""
REPORT_ONLY=false
FORMAT="text"

usage() {
  echo "用法：$0 [OPTIONS]"
  echo ""
  echo "选项："
  echo "  --env ENV            指定环境 (local|71|184)，默认 local"
  echo "  --report-only        仅输出 DEFAULT 表大小报告，跳过其他诊断"
  echo "  --format FORMAT      --report-only 模式下的输出格式 (text|json|csv)，默认 text"
  echo "  --help, -h           显示帮助"
  echo ""
  echo "示例："
  echo "  $0 --env 71                              # 完整健康检查"
  echo "  $0 --env 71 --report-only                # 仅大小报告（文本）"
  echo "  $0 --report-only --format json            # JSON 格式"
  echo "  $0 --report-only --format csv             # CSV 格式"
  echo ""
  echo "位置参数形式（向后兼容）：$0 [local|71|184]"
  exit "${1:-0}"
}

# 参数解析
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)
      ENV="$2"
      shift 2
      ;;
    --report-only)
      REPORT_ONLY=true
      shift
      ;;
    --format)
      FORMAT="$2"
      shift 2
      ;;
    --help|-h)
      usage 0
      ;;
    local|71|184)
      # 向后兼容：位置参数形式
      ENV="$1"
      shift
      ;;
    *)
      echo "❌ 未知参数: $1" >&2
      usage 1
      ;;
  esac
done

ENV="${ENV:-local}"

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
    echo "用法：$0 [local|71|184]" >&2
    exit 1
    ;;
esac

export PGHOST PGPORT PGUSER PGDATABASE

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ========================================
# 辅助函数
# ========================================

section() {
  echo ""
  echo -e "${BLUE}========================================${NC}"
  echo -e "${BLUE}$1${NC}"
  echo -e "${BLUE}========================================${NC}"
}

warn() {
  echo -e "${YELLOW}⚠️  $1${NC}"
}

error() {
  echo -e "${RED}❌ $1${NC}"
}

ok() {
  echo -e "${GREEN}✅ $1${NC}"
}

# ========================================
# 主逻辑
# ========================================

echo "=== 分区表健康检查报告 ==="
echo "环境: $ENV ($PGHOST:$PGPORT/$PGDATABASE)"
echo "时间: $(date)"
echo ""

# 测试连接
if ! psql -c "SELECT 1" > /dev/null 2>&1; then
  error "无法连接到数据库 $PGHOST:$PGPORT/$PGDATABASE"
  echo "请检查："
  echo "  1. 数据库是否运行"
  echo "  2. 网络连通性"
  echo "  3. 认证信息（PGPASSWORD 环境变量）"
  exit 1
fi

ok "数据库连接成功"

# ========================================
# Report-only 模式：仅输出 DEFAULT 表大小报告
# （合并自 scripts/partition/report-default-sizes.sh）
# ========================================

if [[ "$REPORT_ONLY" == "true" ]]; then
  case "$FORMAT" in
    text)
      echo ""
      echo "=== *_default 表存储使用报告 ==="
      echo "环境: $ENV ($PGHOST:$PGPORT/$PGDATABASE)"
      echo "时间: $(date)"
      echo ""
      psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -x -c "
        SELECT
          tablename AS table_name,
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
        ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
      " 2>/dev/null || {
        echo "错误：无法连接数据库" >&2
        exit 1
      }
      ;;

    json)
      psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
        SELECT
          tablename,
          pg_total_relation_size(schemaname||'.'||tablename) AS total_bytes,
          pg_relation_size(schemaname||'.'||tablename) AS table_bytes,
          pg_indexes_size(schemaname||'.'||tablename) AS indexes_bytes,
          n_tup_ins AS inserts,
          n_tup_upd AS updates,
          n_tup_del AS deletes,
          n_live_tup AS live_rows,
          n_dead_tup AS dead_rows,
          CASE
            WHEN pg_total_relation_size(schemaname||'.'||tablename) > 10737418240 THEN 'CRITICAL'
            WHEN pg_total_relation_size(schemaname||'.'||tablename) > 5368709120 THEN 'WARNING'
            ELSE 'OK'
          END AS status
        FROM pg_stat_user_tables
        WHERE tablename LIKE '%_default'
        ORDER BY total_bytes DESC;
      " 2>/dev/null | awk '
        BEGIN { print "["; first=1 }
        NF==0 { next }
        {
          gsub(/[|]/, "", $0)
          split($0, a, "|")
          for(i in a) gsub(/^[[:space:]]+|[[:space:]]+$/, "", a[i])
          if (!first) print ","
          first=0
          print "  {"
          print "    \"table\": \"" a[1] "\","
          print "    \"total_bytes\": " a[2] ","
          print "    \"table_bytes\": " a[3] ","
          print "    \"indexes_bytes\": " a[4] ","
          print "    \"inserts\": " a[5] ","
          print "    \"updates\": " a[6] ","
          print "    \"deletes\": " a[7] ","
          print "    \"live_rows\": " a[8] ","
          print "    \"dead_rows\": " a[9] ","
          print "    \"status\": \"" a[10] "\""
          print "  }"
        }
        END { print "]" }
      ' 2>/dev/null || {
        echo '{"error": "Failed to connect or query database"}' >&2
        exit 1
      }
      ;;

    csv)
      echo "table,total_bytes,table_bytes,indexes_bytes,inserts,updates,deletes,live_rows,dead_rows,status"
      psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
        SELECT
          tablename,
          pg_total_relation_size(schemaname||'.'||tablename),
          pg_relation_size(schemaname||'.'||tablename),
          pg_indexes_size(schemaname||'.'||tablename),
          n_tup_ins,
          n_tup_upd,
          n_tup_del,
          n_live_tup,
          n_dead_tup,
          CASE
            WHEN pg_total_relation_size(schemaname||'.'||tablename) > 10737418240 THEN 'CRITICAL'
            WHEN pg_total_relation_size(schemaname||'.'||tablename) > 5368709120 THEN 'WARNING'
            ELSE 'OK'
          END
        FROM pg_stat_user_tables
        WHERE tablename LIKE '%_default'
        ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
      " 2>/dev/null | awk -F'|' '{
          gsub(/^ +| +$/,"",$0);
          for (i=1; i<=NF; i++) gsub(/^ +| +$/,"",$i);
          print $1","$2","$3","$4","$5","$6","$7","$8","$9","$10
        }' || {
        echo "错误：无法生成 CSV" >&2
        exit 1
      }
      ;;

    *)
      echo "错误：未知格式 '$FORMAT'（支持 text|json|csv）" >&2
      exit 1
      ;;
  esac

  # 文本模式附加告警检查
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

  exit 0
fi

# ========================================
# 完整健康检查模式：原有 6 节诊断
# ========================================

# ========================================
# 1. DEFAULT 表大小和统计
# ========================================

section "1. DEFAULT 表大小和写入统计"

psql -x -c "
  SELECT 
    tablename AS table_name,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS total_size,
    pg_size_pretty(pg_relation_size(schemaname||'.'||tablename)) AS table_size,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename) - pg_relation_size(schemaname||'.'||tablename)) AS indexes_size,
    n_tup_ins AS inserts,
    n_tup_upd AS updates,
    n_tup_del AS deletes,
    n_live_tup AS live_rows,
    n_dead_tup AS dead_rows,
    CASE 
      WHEN n_live_tup > 0 THEN round(100.0 * n_dead_tup / n_live_tup, 2)
      ELSE 0
    END AS dead_ratio_pct
  FROM pg_stat_user_tables
  WHERE tablename LIKE '%_default'
  ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
" 2>/dev/null || error "查询 DEFAULT 表统计失败"

# 检查告警条件
echo ""
echo "📊 健康检查："
psql -t -c "
  SELECT tablename, pg_total_relation_size(schemaname||'.'||tablename) 
  FROM pg_stat_user_tables 
  WHERE tablename LIKE '%_default'
" 2>/dev/null | while read -r table size; do
  if [[ -z "$table" ]]; then continue; fi
  
  size_gb=$(echo "scale=2; $size / 1024 / 1024 / 1024" | bc)
  
  if (( $(echo "$size_gb > 10" | bc -l) )); then
    error "$table 大小 ${size_gb}GB > 10GB（严重）"
  elif (( $(echo "$size_gb > 5" | bc -l) )); then
    warn "$table 大小 ${size_gb}GB > 5GB（警告）"
  else
    ok "$table 大小 ${size_gb}GB（正常）"
  fi
done

# ========================================
# 2. 分区附加状态
# ========================================

section "2. 分区附加状态（按表分组）"

for parent_table in request_logs request_wal usage_ledger routing_decision_log \
                     credential_model_index request_logs_bodies credit_ledger tool_usage_stats; do
  
  echo ""
  echo "--- $parent_table ---"
  
  psql -c "
    SELECT 
      c.relname AS partition_name,
      CASE 
        WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED'
        ELSE 'DETACHED'
      END AS status,
      pg_get_expr(c.relpartbound, c.oid) AS bounds,
      pg_size_pretty(pg_total_relation_size(c.oid)) AS size
    FROM pg_class c
    LEFT JOIN pg_inherits i ON c.oid = i.inhrelid AND i.inhparent = '$parent_table'::regclass
    WHERE c.relname LIKE '${parent_table}_%'
      AND c.relkind = 'r'
    ORDER BY c.relname;
  " 2>/dev/null || warn "$parent_table 不存在或无分区"
  
done

# ========================================
# 3. 当月分区检查
# ========================================

section "3. 当月分区状态（2026-07）"

current_month="2026_07"
echo "检查 ${current_month} 分区是否正确 DETACHED..."
echo ""

detached_count=0
attached_count=0

for parent_table in request_logs request_wal usage_ledger routing_decision_log \
                     credential_model_index request_logs_bodies credit_ledger tool_usage_stats; do
  
  partition_name="${parent_table}_${current_month}"
  
  status=$(psql -t -c "
    SELECT 
      CASE 
        WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED'
        ELSE 'DETACHED'
      END
    FROM pg_class c
    LEFT JOIN pg_inherits i ON c.oid = i.inhrelid AND i.inhparent = '$parent_table'::regclass
    WHERE c.relname = '$partition_name';
  " 2>/dev/null | tr -d ' ')
  
  if [[ "$status" == "DETACHED" ]]; then
    ok "$partition_name: DETACHED（正确）"
    ((detached_count++))
  elif [[ "$status" == "ATTACHED" ]]; then
    error "$partition_name: ATTACHED（错误！应为 DETACHED）"
    ((attached_count++))
  else
    warn "$partition_name: 不存在"
  fi
done

echo ""
if [[ $attached_count -gt 0 ]]; then
  error "发现 $attached_count 个当月分区仍为 ATTACHED 状态"
  echo "  💡 修复：应用 migration 337 (detach_current_future_partitions.sql)"
else
  ok "所有当月分区状态正确（$detached_count 个 DETACHED）"
fi

# ========================================
# 4. 最近写入活动
# ========================================

section "4. 最近写入活动（按表排序）"

psql -c "
  SELECT 
    schemaname || '.' || tablename AS table_name,
    n_tup_ins + n_tup_upd + n_tup_del AS total_writes,
    n_tup_ins AS inserts,
    n_tup_upd AS updates,
    n_tup_del AS deletes,
    CASE 
      WHEN last_autovacuum IS NOT NULL THEN 
        extract(epoch from (now() - last_autovacuum))::int / 3600 || ' hours ago'
      ELSE 'never'
    END AS last_vacuum
  FROM pg_stat_user_tables
  WHERE tablename LIKE '%_default' OR tablename LIKE '%_2026_%'
  ORDER BY n_tup_ins + n_tup_upd + n_tup_del DESC
  LIMIT 20;
" 2>/dev/null || error "查询写入活动失败"

# ========================================
# 5. Promote 函数状态（如果有日志表）
# ========================================

section "5. Promote 函数执行检查"

# 检查 promote 函数是否存在
echo "检查 promote_*_default_batch 函数..."
promote_count=$(psql -t -c "
  SELECT COUNT(*)
  FROM pg_proc
  WHERE proname LIKE 'promote_%_default_batch';
" 2>/dev/null | tr -d ' ')

if [[ "$promote_count" -eq 8 ]]; then
  ok "8 个 promote 函数已安装"
else
  error "仅发现 $promote_count 个 promote 函数（应为 8 个）"
  echo "  💡 修复：应用 migration 336 和 339"
fi

# 尝试查询 partition_manager 日志表（如果存在）
echo ""
echo "📝 Promote 执行日志（最近 24 小时）："
psql -c "
  SELECT 
    table_name,
    rows_moved,
    execution_time,
    created_at
  FROM partition_promote_log
  WHERE created_at > now() - interval '24 hours'
  ORDER BY created_at DESC
  LIMIT 10;
" 2>/dev/null || warn "partition_promote_log 表不存在（可选功能）"

# ========================================
# 6. 磁盘空间检查
# ========================================

section "6. 数据库磁盘使用"

psql -c "
  SELECT 
    pg_database.datname AS database_name,
    pg_size_pretty(pg_database_size(pg_database.datname)) AS size
  FROM pg_database
  WHERE datname = current_database();
" 2>/dev/null || warn "查询数据库大小失败"

# ========================================
# 总结
# ========================================

section "健康检查总结"

echo ""
if [[ $attached_count -gt 0 ]]; then
  error "发现严重问题：当月分区仍为 ATTACHED"
  echo ""
  echo "🔧 修复步骤："
  echo "  1. 应用 migration 337："
  echo "     psql < db/migrations/337_detach_current_future_partitions.sql"
  echo ""
  echo "  2. 验证修复："
  echo "     $0 $ENV"
  exit 1
else
  ok "所有关键指标正常"
  echo ""
  echo "✨ 下一步："
  echo "  - 定期运行本脚本（建议每天）"
  echo "  - 配置 Prometheus 告警（observability/alerts/partition_health.yml）"
  echo "  - 检查 bg/partition_manager.go 日志确认 promote 正常执行"
  exit 0
fi
