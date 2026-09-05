#!/bin/bash
# ============================================================================
# Migration Executor for Timeout Optimization
# Purpose: 执行超时优化相关的数据库迁移
# Date: 2026-07-22
# Author: AI Agent
# ============================================================================

set -euo pipefail

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATION_DIR="${SCRIPT_DIR}"
LOG_DIR="${SCRIPT_DIR}/logs"
LOG_FILE="${LOG_DIR}/migration_$(date +%Y%m%d_%H%M%S).log"

# 数据库配置（从环境变量读取）
DB_HOST="${DB_HOST:-172.16.2.210}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-postgres}"

# 创建日志目录
mkdir -p "${LOG_DIR}"

# ============================================================================
# 日志函数
# ============================================================================

log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo -e "${timestamp} [${level}] ${message}" | tee -a "${LOG_FILE}"
}

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*" | tee -a "${LOG_FILE}"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*" | tee -a "${LOG_FILE}"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*" | tee -a "${LOG_FILE}"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" | tee -a "${LOG_FILE}"
}

# ============================================================================
# 检查前置条件
# ============================================================================

check_prerequisites() {
    log_info "检查前置条件..."
    
    # 检查psql命令
    if ! command -v psql &> /dev/null; then
        log_error "psql命令未找到，请安装PostgreSQL客户端"
        exit 1
    fi
    
    # 检查数据库连接
    if ! psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -c "SELECT 1" &> /dev/null; then
        log_error "无法连接到数据库 ${DB_HOST}:${DB_PORT}/${DB_NAME}"
        log_error "请检查数据库配置和网络连接"
        exit 1
    fi
    
    log_success "前置条件检查通过"
}

# ============================================================================
# 备份数据库
# ============================================================================

backup_database() {
    log_info "备份数据库..."
    
    local backup_file="${LOG_DIR}/backup_before_migration_$(date +%Y%m%d_%H%M%S).sql"
    
    # 只备份相关表
    pg_dump -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" \
        -t request_logs \
        -t system_settings \
        -t session_last_requests \
        --if-exists \
        > "${backup_file}" 2>&1
    
    if [ $? -eq 0 ]; then
        log_success "数据库备份完成: ${backup_file}"
        echo "${backup_file}"
    else
        log_warning "备份失败，但继续执行迁移"
        echo ""
    fi
}

# ============================================================================
# 执行单个迁移
# ============================================================================

execute_migration() {
    local migration_file=$1
    local migration_name=$(basename "${migration_file}" .sql)
    
    log_info "执行迁移: ${migration_name}"
    
    # 执行SQL文件
    psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" \
        -f "${migration_file}" \
        2>&1 | tee -a "${LOG_FILE}"
    
    local exit_code=${PIPESTATUS[0]}
    
    if [ ${exit_code} -eq 0 ]; then
        log_success "迁移 ${migration_name} 执行成功"
        return 0
    else
        log_error "迁移 ${migration_name} 执行失败"
        return 1
    fi
}

# ============================================================================
# 执行所有迁移
# ============================================================================

execute_all_migrations() {
    log_info "开始执行迁移..."
    
    local migrations=(
        "001_create_system_settings.sql"
        "002_extend_request_logs.sql"
        "003_create_session_last_requests.sql"
    )
    
    local success_count=0
    local failed_count=0
    
    for migration in "${migrations[@]}"; do
        local migration_file="${MIGRATION_DIR}/${migration}"
        
        if [ ! -f "${migration_file}" ]; then
            log_error "迁移文件不存在: ${migration_file}"
            ((failed_count++))
            continue
        fi
        
        if execute_migration "${migration_file}"; then
            ((success_count++))
        else
            ((failed_count++))
            log_error "迁移失败，停止执行"
            return 1
        fi
        
        log_info "---"
    done
    
    log_success "迁移完成: ${success_count} 成功, ${failed_count} 失败"
    
    if [ ${failed_count} -gt 0 ]; then
        return 1
    fi
    
    return 0
}

# ============================================================================
# 验证迁移结果
# ============================================================================

verify_migrations() {
    log_info "验证迁移结果..."
    
    # 验证表是否存在
    local tables=("system_settings" "session_last_requests")
    
    for table in "${tables[@]}"; do
        local exists=$(psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" \
            -t -c "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = '${table}');" 2>&1)
        
        if echo "${exists}" | grep -q "t"; then
            log_success "表 ${table} 验证通过"
        else
            log_error "表 ${table} 不存在"
            return 1
        fi
    done
    
    # 验证request_logs新字段
    local columns=("effective_timeout_seconds" "is_continuation" "cached_response_id")
    
    for column in "${columns[@]}"; do
        local exists=$(psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" \
            -t -c "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = '${column}');" 2>&1)
        
        if echo "${exists}" | grep -q "t"; then
            log_success "字段 request_logs.${column} 验证通过"
        else
            log_error "字段 request_logs.${column} 不存在"
            return 1
        fi
    done
    
    log_success "所有验证通过"
    return 0
}

# ============================================================================
# 显示配置摘要
# ============================================================================

show_config_summary() {
    log_info "查询配置摘要..."
    
    psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" \
        -c "SELECT category, COUNT(*) as config_count FROM system_settings GROUP BY category ORDER BY category;" \
        2>&1 | tee -a "${LOG_FILE}"
}

# ============================================================================
# 主函数
# ============================================================================

main() {
    echo "============================================================================"
    echo "  超时优化数据库迁移"
    echo "============================================================================"
    echo "  数据库: ${DB_HOST}:${DB_PORT}/${DB_NAME}"
    echo "  用户: ${DB_USER}"
    echo "  日志: ${LOG_FILE}"
    echo "============================================================================"
    echo ""
    
    # 检查前置条件
    check_prerequisites
    
    # 备份数据库
    BACKUP_FILE=$(backup_database)
    if [ -n "${BACKUP_FILE}" ]; then
        log_info "备份文件: ${BACKUP_FILE}"
    fi
    
    echo ""
    
    # 确认执行
    read -p "是否继续执行迁移? (yes/no): " confirm
    if [ "${confirm}" != "yes" ]; then
        log_warning "用户取消执行"
        exit 0
    fi
    
    echo ""
    
    # 执行迁移
    if execute_all_migrations; then
        echo ""
        
        # 验证结果
        if verify_migrations; then
            echo ""
            
            # 显示配置摘要
            show_config_summary
            
            echo ""
            log_success "✅ 迁移成功完成！"
            log_info "日志文件: ${LOG_FILE}"
            
            if [ -n "${BACKUP_FILE}" ]; then
                log_info "备份文件: ${BACKUP_FILE}"
            fi
            
            exit 0
        else
            log_error "验证失败，请检查日志"
            exit 1
        fi
    else
        log_error "迁移失败，请检查日志"
        
        if [ -n "${BACKUP_FILE}" ]; then
            log_info "可以使用以下命令恢复数据库:"
            log_info "psql -h ${DB_HOST} -p ${DB_PORT} -U ${DB_USER} -d ${DB_NAME} < ${BACKUP_FILE}"
        fi
        
        exit 1
    fi
}

# 执行主函数
main "$@"
