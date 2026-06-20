#!/bin/bash
# cleanup-request-logs.sh
# 统一的数据清理主脚本，每天凌晨 2:00 自动执行

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认配置
LOG_FILE="${LOG_FILE:-/var/log/llm-gateway-cleanup.log}"
ENABLE_TRIM="${ENABLE_TRIM:-false}"           # 是否启用温数据裁剪
ENABLE_ARCHIVE="${ENABLE_ARCHIVE:-true}"      # 是否启用冷数据归档
ENABLE_DELETE="${ENABLE_DELETE:-true}"        # 是否启用过期数据删除
TRIM_DAYS="${TRIM_DAYS:-7-30}"                # 温数据时间范围（裁剪大字段）
ARCHIVE_DAYS="${ARCHIVE_DAYS:-30-90}"         # 冷数据时间范围（归档）
DELETE_DAYS="${DELETE_DAYS:-90}"              # 过期数据天数（删除）
DRY_RUN="${DRY_RUN:-false}"                   # 预览模式

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

function log_info() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] [INFO] $*"
    echo -e "${BLUE}${msg}${NC}"
    echo "$msg" >> "$LOG_FILE"
}

function log_success() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] [SUCCESS] $*"
    echo -e "${GREEN}${msg}${NC}"
    echo "$msg" >> "$LOG_FILE"
}

function log_warn() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] [WARN] $*"
    echo -e "${YELLOW}${msg}${NC}"
    echo "$msg" >> "$LOG_FILE"
}

function log_error() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] [ERROR] $*"
    echo -e "${RED}${msg}${NC}"
    echo "$msg" >> "$LOG_FILE"
}

# 创建日志目录
mkdir -p "$(dirname "$LOG_FILE")"

log_info "=========================================="
log_info "数据清理任务开始"
log_info "=========================================="
log_info "配置："
log_info "  温数据裁剪: $ENABLE_TRIM ($TRIM_DAYS 天)"
log_info "  冷数据归档: $ENABLE_ARCHIVE ($ARCHIVE_DAYS 天)"
log_info "  过期数据删除: $ENABLE_DELETE (>$DELETE_DAYS 天)"
log_info "  预览模式: $DRY_RUN"
echo ""

# 记录开始时间
START_TIME=$(date +%s)
TOTAL_FREED_SPACE=0
TOTAL_ERRORS=0

# Step 1: 分析当前数据量（总是执行）
log_info "Step 1: 分析当前数据量..."
if "$SCRIPT_DIR/analyze-request-logs-size.sh" >> "$LOG_FILE" 2>&1; then
    log_success "数据分析完成"
else
    log_error "数据分析失败"
    TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
fi
echo ""

# Step 2: 裁剪温数据（可选）
if [[ "$ENABLE_TRIM" == "true" ]]; then
    log_info "Step 2: 裁剪温数据大字段 ($TRIM_DAYS 天)..."
    log_warn "⚠️  温数据裁剪功能尚未实施"
    log_warn "⚠️  需要在 Phase 3 实现 trim 操作"
    echo ""
else
    log_info "Step 2: 跳过温数据裁剪（已禁用）"
    echo ""
fi

# Step 3: 归档冷数据
if [[ "$ENABLE_ARCHIVE" == "true" ]]; then
    log_info "Step 3: 归档冷数据 ($ARCHIVE_DAYS 天)..."
    
    ARCHIVE_ARGS="--days $ARCHIVE_DAYS"
    if [[ "$DRY_RUN" == "true" ]]; then
        ARCHIVE_ARGS="$ARCHIVE_ARGS --dry-run"
    fi
    
    if "$SCRIPT_DIR/archive-request-logs.sh" $ARCHIVE_ARGS >> "$LOG_FILE" 2>&1; then
        log_success "冷数据归档完成"
        
        # 如果不是 dry-run，估算释放的空间
        if [[ "$DRY_RUN" == "false" ]]; then
            # TODO: 从归档脚本输出中提取实际释放的空间
            log_info "归档文件已保存到 /opt/llm-gateway-archive/"
        fi
    else
        log_error "冷数据归档失败"
        TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
    fi
    echo ""
else
    log_info "Step 3: 跳过冷数据归档（已禁用）"
    echo ""
fi

# Step 4: 删除过期数据
if [[ "$ENABLE_DELETE" == "true" ]]; then
    log_info "Step 4: 删除过期数据 (>$DELETE_DAYS 天)..."
    
    DELETE_ARGS="--older-than $DELETE_DAYS"
    if [[ "$DRY_RUN" == "true" ]]; then
        DELETE_ARGS="$DELETE_ARGS --dry-run"
    else
        DELETE_ARGS="$DELETE_ARGS --confirm"
        # 自动确认模式：通过管道输入 DELETE
        echo "DELETE" | "$SCRIPT_DIR/delete-old-request-logs.sh" $DELETE_ARGS >> "$LOG_FILE" 2>&1
        DELETE_EXIT=$?
    fi
    
    if [[ "$DRY_RUN" == "true" ]]; then
        "$SCRIPT_DIR/delete-old-request-logs.sh" $DELETE_ARGS >> "$LOG_FILE" 2>&1
        DELETE_EXIT=$?
    fi
    
    if [[ $DELETE_EXIT -eq 0 ]]; then
        log_success "过期数据删除完成"
    else
        log_error "过期数据删除失败"
        TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
    fi
    echo ""
else
    log_info "Step 4: 跳过过期数据删除（已禁用）"
    echo ""
fi

# Step 5: 最终数据量分析
log_info "Step 5: 最终数据量分析..."
if "$SCRIPT_DIR/analyze-request-logs-size.sh" >> "$LOG_FILE" 2>&1; then
    log_success "最终数据分析完成"
else
    log_error "最终数据分析失败"
    TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
fi
echo ""

# 记录结束时间
END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
DURATION_MIN=$((DURATION / 60))
DURATION_SEC=$((DURATION % 60))

# 总结
log_info "=========================================="
log_info "数据清理任务完成"
log_info "=========================================="
log_info "总耗时: ${DURATION_MIN}分${DURATION_SEC}秒"
log_info "错误数: $TOTAL_ERRORS"
if [[ $TOTAL_ERRORS -eq 0 ]]; then
    log_success "✅ 所有任务执行成功"
    exit 0
else
    log_error "❌ 部分任务执行失败，请检查日志: $LOG_FILE"
    exit 1
fi
