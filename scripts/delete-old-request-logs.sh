#!/bin/bash
# delete-old-request-logs.sh
# 删除过期的 request_logs 数据

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认配置
DB_HOST="${DB_HOST:-172.31.0.4}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-stockuser}"
DB_PASSWORD="${DB_PASSWORD:-184_stock_pass_change_me}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

function log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
function log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
function log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
function log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

function usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

删除过期的 request_logs 数据

Options:
    --older-than DAYS   删除 N 天之前的数据
    --before DATE       删除指定日期之前的数据 (YYYY-MM-DD)
    --dry-run           预览模式，只统计不删除
    --confirm           确认删除（生产环境必须）
    --batch-size N      批量删除大小 (default: 10000)
    -h, --help          显示帮助

Examples:
    # 预览：查看 90 天前的数据量
    $0 --older-than 90 --dry-run

    # 删除 90 天前的数据（需要确认）
    $0 --older-than 90 --confirm

    # 删除 2026-01-01 之前的数据
    $0 --before 2026-01-01 --confirm
EOF
    exit 1
}

# 参数解析
OLDER_THAN_DAYS=""
BEFORE_DATE=""
DRY_RUN=true
BATCH_SIZE=10000

while [[ $# -gt 0 ]]; do
    case $1 in
        --older-than) OLDER_THAN_DAYS="$2"; shift 2 ;;
        --before) BEFORE_DATE="$2"; shift 2 ;;
        --dry-run) DRY_RUN=true; shift ;;
        --confirm) DRY_RUN=false; shift ;;
        --batch-size) BATCH_SIZE="$2"; shift 2 ;;
        -h|--help) usage ;;
        *) log_error "Unknown option: $1"; usage ;;
    esac
done

# 验证参数
if [[ -z "$OLDER_THAN_DAYS" && -z "$BEFORE_DATE" ]]; then
    log_error "必须指定 --older-than 或 --before"
    usage
fi

# 计算删除日期
if [[ -n "$OLDER_THAN_DAYS" ]]; then
    DELETE_BEFORE=$(date -u -v-${OLDER_THAN_DAYS}d +%Y-%m-%d 2>/dev/null || date -u -d "${OLDER_THAN_DAYS} days ago" +%Y-%m-%d)
else
    DELETE_BEFORE="$BEFORE_DATE"
fi

log_info "删除配置:"
log_info "  删除日期之前的数据: $DELETE_BEFORE"
log_info "  批量大小: $BATCH_SIZE"
log_info "  预览模式: $DRY_RUN"
echo ""

# 统计待删除数据
log_info "正在统计待删除数据..."
ROW_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
    "SELECT COUNT(*) FROM request_logs WHERE ts < '$DELETE_BEFORE'::date;")

if [[ "$ROW_COUNT" -eq 0 ]]; then
    log_success "没有数据需要删除"
    exit 0
fi

ESTIMATED_SIZE=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
    "SELECT pg_size_pretty((pg_total_relation_size('request_logs') * $ROW_COUNT::numeric / NULLIF(COUNT(*), 0))::bigint) FROM request_logs;")

log_warn "待删除数据: $ROW_COUNT 行，预计释放 $ESTIMATED_SIZE"

# 按租户统计
log_info "按租户统计待删除数据:"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
    "SELECT 
        COALESCE(tenant_id, 'default') AS tenant,
        COUNT(*) AS rows_to_delete
    FROM request_logs 
    WHERE ts < '$DELETE_BEFORE'::date
    GROUP BY tenant_id
    ORDER BY rows_to_delete DESC
    LIMIT 10;"

echo ""

if [[ "$DRY_RUN" == true ]]; then
    log_warn "预览模式，不执行实际删除"
    log_info "要执行删除，请添加 --confirm 参数"
    exit 0
fi

# 二次确认
log_error "⚠️  警告：即将删除 $ROW_COUNT 行数据！"
log_error "⚠️  此操作不可逆！"
echo ""
read -p "确认删除? 输入 'DELETE' 继续: " CONFIRM

if [[ "$CONFIRM" != "DELETE" ]]; then
    log_info "已取消删除操作"
    exit 0
fi

# 执行批量删除
log_info "开始批量删除..."
START_TIME=$(date +%s)
TOTAL_DELETED=0
BATCH_NUM=0

while true; do
    BATCH_NUM=$((BATCH_NUM + 1))
    
    DELETED=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
        "WITH deleted AS (
            DELETE FROM request_logs 
            WHERE id IN (
                SELECT id FROM request_logs 
                WHERE ts < '$DELETE_BEFORE'::date
                LIMIT $BATCH_SIZE
            )
            RETURNING 1
        )
        SELECT COUNT(*) FROM deleted;")
    
    TOTAL_DELETED=$((TOTAL_DELETED + DELETED))
    
    if [[ "$DELETED" -eq 0 ]]; then
        break
    fi
    
    PROGRESS=$(echo "scale=2; $TOTAL_DELETED * 100 / $ROW_COUNT" | bc)
    log_info "批次 $BATCH_NUM: 已删除 $DELETED 行，总计 $TOTAL_DELETED / $ROW_COUNT (${PROGRESS}%)"
    
    # 短暂休眠避免过载
    sleep 0.5
done

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

log_success "删除完成!"
log_info "  总删除行数: $TOTAL_DELETED"
log_info "  耗时: ${DURATION}s"
echo ""

# VACUUM 回收空间
log_info "正在回收磁盘空间 (VACUUM FULL)..."
VACUUM_START=$(date +%s)

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
    "VACUUM FULL ANALYZE request_logs;"

VACUUM_END=$(date +%s)
VACUUM_DURATION=$((VACUUM_END - VACUUM_START))

log_success "空间回收完成 (耗时: ${VACUUM_DURATION}s)"
echo ""

# 显示最终表大小
FINAL_SIZE=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
    "SELECT pg_size_pretty(pg_total_relation_size('request_logs'));")

log_success "删除任务完成"
log_info "当前表大小: $FINAL_SIZE"
