#!/bin/bash
# 
# 154 生产环境快速回滚脚本
# 
# 功能: 快速回滚到上一个稳定版本
# 作者: DevOps Team
# 日期: 2026-08-29
# 版本: v1.0
#

set -euo pipefail

# ==================== 配置区 ====================

# 生产环境配置
PROD_HOST="${PROD_HOST:-154.XXX.XXX.XXX}"  # 请设置实际 IP
PROD_PORT="${PROD_PORT:-22}"
PROD_USER="${PROD_USER:-root}"
SERVICE_NAME="llmgo-154.service"
DEPLOY_DIR="/opt/llm-gateway-go"
BINARY_NAME="gateway"
API_PORT="8781"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ==================== 函数定义 ====================

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# 执行远程命令
remote_exec() {
    ssh -p "$PROD_PORT" "$PROD_USER@$PROD_HOST" "$@"
}

# 确认操作
confirm() {
    local prompt="$1"
    local default="${2:-n}"
    
    if [[ "$default" == "y" ]]; then
        prompt="$prompt [Y/n]: "
    else
        prompt="$prompt [y/N]: "
    fi
    
    read -rp "$prompt" response
    response=${response,,} # 转小写
    
    if [[ -z "$response" ]]; then
        response="$default"
    fi
    
    [[ "$response" == "y" ]]
}

# ==================== 检查备份 ====================

check_backup_exists() {
    log_info "=========================================="
    log_info "检查可用备份"
    log_info "=========================================="
    
    log_info "查询备份文件..."
    local backups=$(remote_exec "cd $DEPLOY_DIR && ls -t ${BINARY_NAME}.backup.* 2>/dev/null || echo ''")
    
    if [[ -z "$backups" ]]; then
        log_error "未找到任何备份文件"
        log_error "备份路径: $DEPLOY_DIR/${BINARY_NAME}.backup.*"
        exit 1
    fi
    
    log_success "找到以下备份:"
    echo "$backups" | nl -w2 -s'. '
    echo ""
    
    # 获取最新备份
    local latest_backup=$(echo "$backups" | head -1)
    echo "$latest_backup"
}

# ==================== 显示当前状态 ====================

show_current_status() {
    log_info "=========================================="
    log_info "当前系统状态"
    log_info "=========================================="
    
    # 服务状态
    log_info "检查服务状态..."
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "服务状态: running"
    else
        log_error "服务状态: not running"
    fi
    
    # 当前版本
    log_info "检查当前版本..."
    local version=$(curl -s -m 5 "http://${PROD_HOST}:${API_PORT}/api/system/version" 2>/dev/null || echo "")
    if [[ -n "$version" ]]; then
        echo "$version" | jq -r '. | "  版本: \(.version // "unknown"), 构建时间: \(.build_time // "unknown")"' 2>/dev/null || echo "  $version"
    else
        log_warning "无法获取版本信息（服务可能已停止）"
    fi
    
    # 最近错误
    log_info "检查最近错误日志（5 分钟内）..."
    local error_count=$(remote_exec "journalctl -u $SERVICE_NAME --since '5 minutes ago' --no-pager | grep -c -E '(ERROR|FATAL)' || echo 0")
    echo "  错误日志数量: $error_count"
    
    if [[ $error_count -gt 0 && $error_count -le 10 ]]; then
        log_warning "最近的错误日志:"
        remote_exec "journalctl -u $SERVICE_NAME --since '5 minutes ago' --no-pager | grep -E '(ERROR|FATAL)' | tail -5" | sed 's/^/    /'
    fi
    
    echo ""
}

# ==================== 执行回滚 ====================

perform_rollback() {
    local backup_name="$1"
    
    log_warning "=========================================="
    log_warning "开始回滚部署"
    log_warning "=========================================="
    
    echo "即将回滚到备份: $backup_name"
    echo ""
    
    if [[ "${FORCE_ROLLBACK:-false}" != "true" ]]; then
        if ! confirm "确认执行回滚？" "y"; then
            log_info "回滚已取消"
            exit 0
        fi
    fi
    
    echo ""
    
    # 步骤 1: 停止服务
    log_info "步骤 1/5: 停止服务..."
    if remote_exec "systemctl stop $SERVICE_NAME"; then
        log_success "服务已停止"
    else
        log_error "停止服务失败"
        exit 1
    fi
    
    # 步骤 2: 保存失败的版本
    log_info "步骤 2/5: 保存失败的版本..."
    local failed_name="${BINARY_NAME}.failed.$(date +%Y%m%d_%H%M%S)"
    if remote_exec "cd $DEPLOY_DIR && mv $BINARY_NAME $failed_name"; then
        log_success "失败版本已保存: $failed_name"
    else
        log_warning "保存失败版本时出错（继续回滚）"
    fi
    
    # 步骤 3: 恢复备份
    log_info "步骤 3/5: 恢复备份..."
    if remote_exec "cd $DEPLOY_DIR && cp $backup_name $BINARY_NAME"; then
        log_success "备份已恢复"
    else
        log_error "恢复备份失败"
        log_error "尝试紧急恢复..."
        remote_exec "cd $DEPLOY_DIR && mv $failed_name $BINARY_NAME" || true
        exit 1
    fi
    
    # 步骤 4: 设置权限
    log_info "步骤 4/5: 设置执行权限..."
    remote_exec "chmod +x $DEPLOY_DIR/$BINARY_NAME"
    log_success "权限已设置"
    
    # 步骤 5: 启动服务
    log_info "步骤 5/5: 启动服务..."
    if remote_exec "systemctl start $SERVICE_NAME"; then
        log_success "服务已启动"
    else
        log_error "启动服务失败"
        exit 1
    fi
    
    # 等待服务启动
    log_info "等待服务启动（5 秒）..."
    sleep 5
}

# ==================== 验证回滚 ====================

verify_rollback() {
    log_info "=========================================="
    log_info "验证回滚结果"
    log_info "=========================================="
    
    local all_passed=true
    
    # 1. 检查服务状态
    log_info "1. 检查服务状态..."
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "服务状态: running"
    else
        log_error "服务状态: not running"
        all_passed=false
    fi
    
    # 2. 检查版本
    log_info "2. 检查版本..."
    local version=$(curl -s -m 5 "http://${PROD_HOST}:${API_PORT}/api/system/version" 2>/dev/null || echo "")
    if [[ -n "$version" ]]; then
        log_success "版本接口: 可访问"
        echo "$version" | jq . 2>/dev/null || echo "$version"
    else
        log_error "版本接口: 无响应"
        all_passed=false
    fi
    
    # 3. 检查健康状态
    log_info "3. 检查健康状态..."
    local health=$(curl -s -m 5 "http://${PROD_HOST}:${API_PORT}/api/system/health" 2>/dev/null || echo "")
    if [[ -n "$health" ]] && echo "$health" | grep -q "healthy"; then
        log_success "健康检查: healthy"
    else
        log_error "健康检查: unhealthy 或无响应"
        all_passed=false
    fi
    
    # 4. 检查启动日志
    log_info "4. 检查启动日志..."
    local error_count=$(remote_exec "journalctl -u $SERVICE_NAME --since '1 minute ago' --no-pager | grep -c -E '(ERROR|FATAL)' || echo 0")
    if [[ $error_count -eq 0 ]]; then
        log_success "启动日志: 无错误"
    else
        log_warning "启动日志: 发现 $error_count 条错误"
        remote_exec "journalctl -u $SERVICE_NAME --since '1 minute ago' --no-pager | grep -E '(ERROR|FATAL)' | head -5" | sed 's/^/  /'
    fi
    
    echo ""
    
    if [[ "$all_passed" == "true" ]]; then
        log_success "回滚验证: 全部通过"
        return 0
    else
        log_error "回滚验证: 部分检查失败"
        return 1
    fi
}

# ==================== 回滚后操作建议 ====================

print_post_rollback_actions() {
    log_info "=========================================="
    log_info "回滚后操作建议"
    log_info "=========================================="
    
    echo ""
    echo "1. 持续监控服务状态（至少 30 分钟）:"
    echo "   ./scripts/health-check-154.sh"
    echo ""
    echo "2. 检查实时日志:"
    echo "   ssh -p $PROD_PORT $PROD_USER@$PROD_HOST \"journalctl -u $SERVICE_NAME -f\""
    echo ""
    echo "3. 检查最近请求成功率:"
    echo "   ssh -p $PROD_PORT $PROD_USER@$PROD_HOST 'PGPASSWORD=\"4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg\" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c \"SELECT COUNT(*) FILTER (WHERE success = true) * 100.0 / COUNT(*) AS success_rate FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '\"'\"'10 minutes'\"'\"';\"'"
    echo ""
    echo "4. 保存失败日志用于分析:"
    echo "   ssh -p $PROD_PORT $PROD_USER@$PROD_HOST \"journalctl -u $SERVICE_NAME --since '30 minutes ago' --no-pager\" > rollback_logs_\$(date +%Y%m%d_%H%M%S).log"
    echo ""
    echo "5. 创建问题报告:"
    echo "   - 记录回滚原因"
    echo "   - 记录失败现象"
    echo "   - 附加相关日志"
    echo "   - 通知相关团队"
    echo ""
    echo "6. 分析失败原因:"
    echo "   - 检查失败版本的日志"
    echo "   - 分析错误模式"
    echo "   - 确定修复方案"
    echo ""
}

# ==================== 选择备份 ====================

select_backup() {
    local backups=$(remote_exec "cd $DEPLOY_DIR && ls -t ${BINARY_NAME}.backup.* 2>/dev/null || echo ''")
    
    if [[ -z "$backups" ]]; then
        return 1
    fi
    
    local backup_count=$(echo "$backups" | wc -l)
    
    if [[ $backup_count -eq 1 ]]; then
        echo "$backups"
        return 0
    fi
    
    # 如果有多个备份，让用户选择
    if [[ "${AUTO_SELECT:-true}" == "true" ]]; then
        # 自动选择最新的
        echo "$backups" | head -1
        return 0
    fi
    
    echo "发现多个备份，请选择:"
    echo "$backups" | nl -w2 -s'. '
    echo ""
    
    local selection
    while true; do
        read -rp "请输入备份编号 [1]: " selection
        selection=${selection:-1}
        
        if [[ "$selection" =~ ^[0-9]+$ ]] && [[ $selection -ge 1 && $selection -le $backup_count ]]; then
            echo "$backups" | sed -n "${selection}p"
            return 0
        else
            echo "无效的选择，请重试"
        fi
    done
}

# ==================== 主流程 ====================

main() {
    local start_time=$(date +%s)
    
    log_warning "=========================================="
    log_warning "154 生产环境回滚脚本"
    log_warning "时间: $(date '+%Y-%m-%d %H:%M:%S')"
    log_warning "=========================================="
    echo ""
    
    # 显示当前状态
    show_current_status
    
    # 检查备份
    local backup_name=$(check_backup_exists)
    
    if [[ -z "$backup_name" ]]; then
        log_error "无法确定备份文件"
        exit 1
    fi
    
    # 获取备份信息
    log_info "选择的备份: $backup_name"
    local backup_size=$(remote_exec "stat -c%s $DEPLOY_DIR/$backup_name" 2>/dev/null || echo "unknown")
    local backup_time=$(remote_exec "stat -c%y $DEPLOY_DIR/$backup_name" 2>/dev/null | cut -d'.' -f1 || echo "unknown")
    echo "  大小: $backup_size bytes"
    echo "  时间: $backup_time"
    echo ""
    
    # 执行回滚
    perform_rollback "$backup_name"
    
    echo ""
    
    # 验证回滚
    if verify_rollback; then
        local end_time=$(date +%s)
        local duration=$((end_time - start_time))
        
        echo ""
        log_success "=========================================="
        log_success "回滚完成！"
        log_success "总耗时: ${duration} 秒"
        log_success "=========================================="
        echo ""
        
        print_post_rollback_actions
        exit 0
    else
        log_error "=========================================="
        log_error "回滚验证失败"
        log_error "请立即检查服务状态"
        log_error "=========================================="
        exit 1
    fi
}

# ==================== 脚本入口 ====================

# 显示使用帮助
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    echo "用法: $0 [选项]"
    echo ""
    echo "选项:"
    echo "  -h, --help     显示此帮助信息"
    echo "  -f, --force    强制回滚（不询问确认）"
    echo ""
    echo "环境变量:"
    echo "  PROD_HOST      生产环境 IP（默认: 154.XXX.XXX.XXX）"
    echo "  PROD_PORT      SSH 端口（默认: 22）"
    echo "  PROD_USER      SSH 用户（默认: root）"
    echo "  FORCE_ROLLBACK 强制回滚（默认: false）"
    echo "  AUTO_SELECT    自动选择最新备份（默认: true）"
    echo ""
    echo "示例:"
    echo "  $0                    # 交互式回滚"
    echo "  $0 -f                 # 强制回滚（不询问）"
    echo "  PROD_HOST=1.2.3.4 $0  # 指定目标主机"
    exit 0
fi

# 处理命令行参数
if [[ "${1:-}" == "-f" || "${1:-}" == "--force" ]]; then
    FORCE_ROLLBACK=true
fi

# 捕获错误
trap 'log_error "回滚过程中发生错误，退出码: $?"' ERR

# 执行主流程
main "$@"
