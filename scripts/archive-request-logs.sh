#!/bin/bash
# archive-request-logs.sh
# 归档指定时间段的 request_logs 数据到压缩文件

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认配置
DB_HOST="${DB_HOST:-172.31.0.4}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-stockuser}"
DB_PASSWORD="${DB_PASSWORD:-184_stock_pass_change_me}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/opt/llm-gateway-archive}"
FORMAT="${FORMAT:-jsonl}"  # jsonl | sql

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

归档 request_logs 数据到压缩文件

Options:
    --from DATE         起始日期 (YYYY-MM-DD)
    --to DATE           结束日期 (YYYY-MM-DD)
    --days N            归档 N 天前到 N-M 天前的数据（与 --from/--to 互斥）
    --format FORMAT     归档格式: jsonl (default) | sql
    --archive-dir DIR   归档目录 (default: /opt/llm-gateway-archive)
    --dry-run           预览模式，不实际归档
    --delete            归档后删除源数据（危险！）
    -h, --help          显示帮助

Examples:
    # 归档 2026-03-01 到 2026-04-01 的数据
    $0 --from 2026-03-01 --to 2026-04-01

    # 归档 30-90 天前的数据（冷数据）
    $0 --days 30-90

    # 归档后删除源数据
    $0 --from 2026-03-01 --to 2026-04-01 --delete
EOF
    exit 1
}

# 参数解析
FROM_DATE=""
TO_DATE=""
DAYS_RANGE=""
DRY_RUN=false
DELETE_AFTER=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --from) FROM_DATE="$2"; shift 2 ;;
        --to) TO_DATE="$2"; shift 2 ;;
        --days) DAYS_RANGE="$2"; shift 2 ;;
        --format) FORMAT="$2"; shift 2 ;;
        --archive-dir) ARCHIVE_DIR="$2"; shift 2 ;;
        --dry-run) DRY_RUN=true; shift ;;
        --delete) DELETE_AFTER=true; shift ;;
        -h|--help) usage ;;
        *) log_error "Unknown option: $1"; usage ;;
    esac
done

# 验证参数
if [[ -z "$FROM_DATE" && -z "$DAYS_RANGE" ]]; then
    log_error "必须指定 --from/--to 或 --days"
    usage
fi

if [[ -n "$DAYS_RANGE" ]]; then
    if [[ "$DAYS_RANGE" =~ ^([0-9]+)-([0-9]+)$ ]]; then
        FROM_DAYS="${BASH_REMATCH[2]}"
        TO_DAYS="${BASH_REMATCH[1]}"
        FROM_DATE=$(date -u -v-${FROM_DAYS}d +%Y-%m-%d 2>/dev/null || date -u -d "${FROM_DAYS} days ago" +%Y-%m-%d)
        TO_DATE=$(date -u -v-${TO_DAYS}d +%Y-%m-%d 2>/dev/null || date -u -d "${TO_DAYS} days ago" +%Y-%m-%d)
    else
        log_error "Invalid --days format. Use: N-M (e.g., 30-90)"
        exit 1
    fi
fi

if [[ -z "$TO_DATE" ]]; then
    TO_DATE=$(date -u +%Y-%m-%d)
fi

# 创建归档目录
mkdir -p "$ARCHIVE_DIR"

# 生成归档文件名
ARCHIVE_FILENAME="request_logs_${FROM_DATE}_to_${TO_DATE}"
ARCHIVE_PATH="$ARCHIVE_DIR/${ARCHIVE_FILENAME}"

log_info "归档配置:"
log_info "  时间范围: $FROM_DATE ~ $TO_DATE"
log_info "  格式: $FORMAT"
log_info "  归档目录: $ARCHIVE_DIR"
log_info "  删除源数据: $DELETE_AFTER"
log_info "  预览模式: $DRY_RUN"
echo ""

# 统计待归档数据量
log_info "正在统计待归档数据..."
ROW_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
    "SELECT COUNT(*) FROM request_logs WHERE ts >= '$FROM_DATE'::date AND ts < '$TO_DATE'::date + INTERVAL '1 day';")

if [[ "$ROW_COUNT" -eq 0 ]]; then
    log_warn "没有数据需要归档"
    exit 0
fi

ESTIMATED_SIZE=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
    "SELECT pg_size_pretty((pg_total_relation_size('request_logs') * $ROW_COUNT::numeric / NULLIF(COUNT(*), 0))::bigint) FROM request_logs;")

log_info "待归档数据: $ROW_COUNT 行，预计大小 $ESTIMATED_SIZE"
echo ""

if [[ "$DRY_RUN" == true ]]; then
    log_warn "预览模式，不执行实际归档"
    exit 0
fi

# 执行归档
log_info "开始归档..."
START_TIME=$(date +%s)

if [[ "$FORMAT" == "jsonl" ]]; then
    # JSONL 格式
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A -c \
        "COPY (
            SELECT row_to_json(t)
            FROM (
                SELECT * FROM request_logs 
                WHERE ts >= '$FROM_DATE'::date 
                  AND ts < '$TO_DATE'::date + INTERVAL '1 day'
                ORDER BY ts
            ) t
        ) TO STDOUT" | gzip -9 > "${ARCHIVE_PATH}.jsonl.gz"
    
    ARCHIVE_FILE="${ARCHIVE_PATH}.jsonl.gz"
elif [[ "$FORMAT" == "sql" ]]; then
    # SQL 格式
    PGPASSWORD="$DB_PASSWORD" pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        --table=request_logs \
        --data-only \
        --where="ts >= '$FROM_DATE'::date AND ts < '$TO_DATE'::date + INTERVAL '1 day'" \
        | gzip -9 > "${ARCHIVE_PATH}.sql.gz"
    
    ARCHIVE_FILE="${ARCHIVE_PATH}.sql.gz"
else
    log_error "Unsupported format: $FORMAT"
    exit 1
fi

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
ARCHIVE_SIZE=$(du -h "$ARCHIVE_FILE" | cut -f1)

log_success "归档完成!"
log_info "  文件: $ARCHIVE_FILE"
log_info "  大小: $ARCHIVE_SIZE"
log_info "  耗时: ${DURATION}s"
echo ""

# 验证归档文件
log_info "验证归档文件..."
if [[ "$FORMAT" == "jsonl" ]]; then
    ARCHIVED_ROWS=$(zcat "$ARCHIVE_FILE" | wc -l | tr -d ' ')
elif [[ "$FORMAT" == "sql" ]]; then
    ARCHIVED_ROWS=$(zcat "$ARCHIVE_FILE" | grep -c "^INSERT INTO" || echo "0")
fi

log_info "归档行数: $ARCHIVED_ROWS (原始: $ROW_COUNT)"

if [[ "$ARCHIVED_ROWS" -ne "$ROW_COUNT" ]]; then
    log_error "归档验证失败！行数不匹配"
    exit 1
fi

log_success "归档验证通过"
echo ""

# 删除源数据（如果指定）
if [[ "$DELETE_AFTER" == true ]]; then
    log_warn "准备删除源数据..."
    read -p "确认删除 $ROW_COUNT 行数据? (yes/no): " CONFIRM
    
    if [[ "$CONFIRM" == "yes" ]]; then
        log_info "正在删除源数据..."
        DELETE_START=$(date +%s)
        
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "DELETE FROM request_logs WHERE ts >= '$FROM_DATE'::date AND ts < '$TO_DATE'::date + INTERVAL '1 day';"
        
        DELETE_END=$(date +%s)
        DELETE_DURATION=$((DELETE_END - DELETE_START))
        
        log_success "已删除 $ROW_COUNT 行数据 (耗时: ${DELETE_DURATION}s)"
        
        # VACUUM 回收空间
        log_info "正在回收磁盘空间 (VACUUM)..."
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "VACUUM ANALYZE request_logs;"
        
        log_success "空间回收完成"
    else
        log_info "已取消删除操作"
    fi
fi

echo ""
log_success "归档任务完成"
