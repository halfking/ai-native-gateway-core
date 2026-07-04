#!/bin/bash
# Manual Promote Default to Monthly Partition
#
# 用途：手动将 *_default 表中的数据迁移到月度分区
# 使用场景：
#   - promote 函数失败需要手动干预
#   - 紧急清理大量积压数据
#   - 定期维护时使用
#
# 使用：./scripts/partition/manual-promote-default.sh [--table TABLE] [--retention DAYS] [--batch SIZE]
#
# 示例：
#   # 迁移 request_logs_default 中 7 天前的数据
#   ./scripts/partition/manual-promote-default.sh --table request_logs
#
#   # 迁移 14 天前的数据（更长保留）
#   ./scripts/partition/manual-promote-default.sh --table usage_ledger --retention 14
#
#   # 迁移所有表
#   ./scripts/partition/manual-promote-default.sh --all

set -euo pipefail

# ========================================
# 配置
# ========================================

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-kxuser}"
PGDATABASE="${PGDATABASE:-llm_gateway}"

RETENTION_DAYS="${RETENTION_DAYS:-7}"
BATCH_SIZE="${BATCH_SIZE:-5000}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# ========================================
# 辅助函数
# ========================================

usage() {
  echo "用法：$0 [OPTIONS]"
  echo ""
  echo "选项："
  echo "  --table TABLE      指定表名 (request_logs, usage_ledger, 等)"
  echo "  --retention DAYS   保留天数 (默认: 7)"
  echo "  --batch SIZE       每批大小 (默认: 5000)"
  echo "  --all              迁移所有表"
  echo "  --dry-run          仅显示将执行的 SQL，不实际执行"
  echo "  --help             显示帮助"
  echo ""
  echo "示例："
  echo "  $0 --table request_logs --retention 7"
  echo "  $0 --all --dry-run"
  echo "  RETENTION_DAYS=14 $0 --table usage_ledger"
  exit 1
}

log() {
  echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $*"
}

warn() {
  echo -e "${YELLOW}⚠️ $*${NC}"
}

error() {
  echo -e "${RED}❌ $*${NC}"
}

success() {
  echo -e "${GREEN}✅ $*${NC}"
}

section() {
  echo ""
  echo -e "${CYAN}========================================${NC}"
  echo -e "${CYAN}$1${NC}"
  echo -e "${CYAN}========================================${NC}"
}

# ========================================
# 解析参数
# ========================================

TABLE=""
ALL_TABLES=false
DRY_RUN=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --table)
      TABLE="$2"
      shift 2
      ;;
    --retention)
      RETENTION_DAYS="$2"
      shift 2
      ;;
    --batch)
      BATCH_SIZE="$2"
      shift 2
      ;;
    --all)
      ALL_TABLES=true
      shift
      ;;
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --help|-h)
      usage
      ;;
    *)
      error "未知参数: $1"
      usage
      ;;
  esac
done

if [[ "$ALL_TABLES" == "false" && -z "$TABLE" ]]; then
  error "必须指定 --table 或 --all"
  usage
fi

# ========================================
# 目标表列表
# ========================================

ALL_TABLE_NAMES=(
  "request_logs"
  "request_wal"
  "usage_ledger"
  "routing_decision_log"
  "credential_model_index"
  "request_logs_bodies"
  "credit_ledger"
  "tool_usage_stats"
)

# ========================================
# 核心逻辑：迁移单个表
# ========================================

promote_table() {
  local table_name="$1"
  local retention_days="$2"
  local batch_size="$3"
  
  section "迁移 $table_name"
  log "保留窗口: $retention_days 天"
  log "批次大小: $batch_size"
  
  # 检查表是否存在
  if ! psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT 1 FROM pg_class WHERE relname = '${table_name}_default'
  " | grep -q 1; then
    warn "${table_name}_default 不存在，跳过"
    return 0
  fi
  
  # 确定时间戳列
  local ts_column=""
  case "$table_name" in
    request_logs|usage_ledger|routing_decision_log|request_logs_bodies)
      ts_column="ts"
      ;;
    request_wal)
      ts_column="created_at"
      ;;
    credential_model_index)
      ts_column="bucket"
      ;;
    credit_ledger)
      ts_column="created_at"
      ;;
    tool_usage_stats)
      ts_column="usage_date"
      ;;
    *)
      ts_column="ts"
      ;;
  esac
  
  log "时间戳列: $ts_column"
  
  # 检查有多少数据需要迁移
  local pending_count
  if [[ "$table_name" == "tool_usage_stats" ]]; then
    pending_count=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
      SELECT count(*) FROM ${table_name}_default
      WHERE usage_date < current_date - interval '1 day' * $retention_days
    " | tr -d ' ')
  else
    pending_count=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
      SELECT count(*) FROM ${table_name}_default
      WHERE ${ts_column} < now() - interval '1 day' * $retention_days
    " | tr -d ' ')
  fi
  
  log "待迁移行数: $pending_count"
  
  if [[ "$pending_count" == "0" || "$pending_count" == "" ]]; then
    success "无待迁移数据"
    return 0
  fi
  
  # Dry run
  if [[ "$DRY_RUN" == "true" ]]; then
    warn "[DRY RUN] 将迁移 ${pending_count} 行"
    log "SQL: DELETE FROM ${table_name}_default WHERE ${ts_column} < now() - ${retention_days} days"
    log "     + INSERT INTO ${table_name} SELECT * FROM deleted_rows"
    return 0
  fi
  
  # 实际迁移
  local total_migrated=0
  local batch_num=0
  
  while true; do
    ((batch_num++))
    
    local moved
    if [[ "$table_name" == "tool_usage_stats" ]]; then
      moved=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
        WITH del AS (
          DELETE FROM ${table_name}_default
          WHERE usage_date < current_date - interval '1 day' * $retention_days
          ORDER BY usage_date
          LIMIT $batch_size
          RETURNING *
        ),
        ins AS (
          INSERT INTO ${table_name}
          SELECT * FROM del
          ON CONFLICT DO NOTHING
          RETURNING 1
        )
        SELECT count(*) FROM ins;
      " | tr -d ' ')
    else
      moved=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
        WITH del AS (
          DELETE FROM ${table_name}_default
          WHERE ${ts_column} < now() - interval '1 day' * $retention_days
          ORDER BY ${ts_column}
          LIMIT $batch_size
          RETURNING *
        ),
        ins AS (
          INSERT INTO ${table_name}
          SELECT * FROM del
          ON CONFLICT DO NOTHING
          RETURNING 1
        )
        SELECT count(*) FROM ins;
      " | tr -d ' ')
    fi
    
    if [[ -z "$moved" || "$moved" == "0" ]]; then
      break
    fi
    
    total_migrated=$((total_migrated + moved))
    log "批次 $batch_num: 迁移 $moved 行 (累计: $total_migrated)"
    
    # 安全限制：最多 1000 批次
    if [[ $batch_num -ge 1000 ]]; then
      warn "达到最大批次限制 (1000)，停止迁移"
      break
    fi
  done
  
  success "迁移完成: $total_migrated 行 (共 $batch_num 批次)"
}

# ========================================
# 主逻辑
# ========================================

export PGHOST PGUSER PGDATABASE

section "手动迁移 *_default 到月度分区"
log "环境: $PGHOST:$PGPORT/$PGDATABASE"
log "保留窗口: $RETENTION_DAYS 天"
log "批次大小: $BATCH_SIZE"

if [[ "$DRY_RUN" == "true" ]]; then
  warn "[DRY RUN 模式] 不会实际执行任何操作"
fi

echo ""

if [[ "$ALL_TABLES" == "true" ]]; then
  log "将迁移所有 8 个表..."
  for tbl in "${ALL_TABLE_NAMES[@]}"; do
    promote_table "$tbl" "$RETENTION_DAYS" "$BATCH_SIZE"
  done
else
  promote_table "$TABLE" "$RETENTION_DAYS" "$BATCH_SIZE"
fi

section "迁移完成"

if [[ "$DRY_RUN" == "true" ]]; then
  success "Dry run 完成，未执行实际操作"
else
  success "所有迁移操作完成"
fi

log "验证迁移结果："
log "  SELECT count(*) FROM ${TABLE}_default WHERE ${ts_column} < now() - interval '1 day' * $RETENTION_DAYS"
