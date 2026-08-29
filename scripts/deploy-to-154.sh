#!/bin/bash
# 
# 154 生产环境自动化部署脚本
# 
# 功能: SSE Frame Validation
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

# 数据库配置（用于验证）
DB_HOST="172.16.2.210"
DB_PORT="5432"
DB_NAME="llm_gateway"
DB_USER="llm_gateway"
DB_PASS="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"

# 本地配置
LOCAL_BINARY="${LOCAL_BINARY:-./gateway}"
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

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

# 检查命令是否存在
check_command() {
    if ! command -v "$1" &> /dev/null; then
        log_error "命令 '$1' 未找到，请先安装"
        exit 1
    fi
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

# ==================== 部署前检查 ====================

pre_deployment_checks() {
    log_info "=========================================="
    log_info "开始部署前检查"
    log_info "=========================================="
    
    # 检查必要命令
    log_info "检查必要命令..."
    check_command ssh
    check_command scp
    check_command curl
    
    # 检查本地二进制文件
    log_info "检查本地二进制文件..."
    if [[ ! -f "$LOCAL_BINARY" ]]; then
        log_error "本地二进制文件不存在: $LOCAL_BINARY"
        log_info "提示: 请先运行 'make build' 或 'go build -o gateway ./cmd/gateway'"
        exit 1
    fi
    
    local_size=$(stat -f%z "$LOCAL_BINARY" 2>/dev/null || stat -c%s "$LOCAL_BINARY" 2>/dev/null)
    log_success "本地二进制文件存在，大小: $(numfmt --to=iec-i --suffix=B "$local_size" 2>/dev/null || echo "$local_size bytes")"
    
    # 检查服务器连接
    log_info "检查服务器连接..."
    if ! remote_exec "echo 'Connected'" &>/dev/null; then
        log_error "无法连接到服务器: $PROD_USER@$PROD_HOST:$PROD_PORT"
        exit 1
    fi
    log_success "服务器连接正常"
    
    # 检查服务状态
    log_info "检查当前服务状态..."
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "服务运行正常"
    else
        log_warning "服务未运行或状态异常"
        if ! confirm "是否继续部署？"; then
            log_info "部署已取消"
            exit 0
        fi
    fi
    
    # 检查磁盘空间
    log_info "检查磁盘空间..."
    disk_usage=$(remote_exec "df -h $DEPLOY_DIR | tail -1 | awk '{print \$5}' | sed 's/%//'")
    if [[ $disk_usage -gt 90 ]]; then
        log_warning "磁盘使用率过高: ${disk_usage}%"
        if ! confirm "是否继续部署？"; then
            log_info "部署已取消"
            exit 0
        fi
    else
        log_success "磁盘空间充足，使用率: ${disk_usage}%"
    fi
    
    # 检查数据库连接
    log_info "检查数据库连接..."
    if remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c 'SELECT 1;'" &>/dev/null; then
        log_success "数据库连接正常"
    else
        log_error "数据库连接失败"
        exit 1
    fi
    
    log_success "所有部署前检查通过"
    echo ""
}

# ==================== 备份当前版本 ====================

backup_current_version() {
    log_info "=========================================="
    log_info "备份当前版本"
    log_info "=========================================="
    
    local timestamp=$(date +%Y%m%d_%H%M%S)
    local backup_name="${BINARY_NAME}.backup.${timestamp}"
    
    log_info "创建备份: $backup_name"
    remote_exec "cd $DEPLOY_DIR && cp $BINARY_NAME $backup_name"
    
    # 验证备份
    if remote_exec "test -f $DEPLOY_DIR/$backup_name"; then
        local backup_size=$(remote_exec "stat -c%s $DEPLOY_DIR/$backup_name")
        log_success "备份创建成功: $backup_name ($(numfmt --to=iec-i --suffix=B "$backup_size" 2>/dev/null || echo "$backup_size bytes"))"
        echo "$backup_name" > /tmp/llmgo_backup_name.txt
    else
        log_error "备份创建失败"
        exit 1
    fi
    
    # 清理旧备份（保留最近 5 个）
    log_info "清理旧备份（保留最近 5 个）..."
    remote_exec "cd $DEPLOY_DIR && ls -t ${BINARY_NAME}.backup.* 2>/dev/null | tail -n +6 | xargs rm -f || true"
    
    echo ""
}

# ==================== 上传新版本 ====================

upload_new_version() {
    log_info "=========================================="
    log_info "上传新版本"
    log_info "=========================================="
    
    local temp_name="${BINARY_NAME}.new"
    
    log_info "上传二进制文件到服务器..."
    if scp -P "$PROD_PORT" "$LOCAL_BINARY" "$PROD_USER@$PROD_HOST:$DEPLOY_DIR/$temp_name"; then
        log_success "上传完成"
    else
        log_error "上传失败"
        exit 1
    fi
    
    # 验证上传
    log_info "验证上传文件..."
    local remote_size=$(remote_exec "stat -c%s $DEPLOY_DIR/$temp_name")
    local local_size=$(stat -f%z "$LOCAL_BINARY" 2>/dev/null || stat -c%s "$LOCAL_BINARY" 2>/dev/null)
    
    if [[ "$remote_size" == "$local_size" ]]; then
        log_success "文件大小匹配: $(numfmt --to=iec-i --suffix=B "$local_size" 2>/dev/null || echo "$local_size bytes")"
    else
        log_error "文件大小不匹配! 本地: $local_size, 远程: $remote_size"
        exit 1
    fi
    
    # 设置执行权限
    remote_exec "chmod +x $DEPLOY_DIR/$temp_name"
    
    echo ""
}

# ==================== 部署新版本 ====================

deploy_new_version() {
    log_info "=========================================="
    log_info "部署新版本并重启服务"
    log_info "=========================================="
    
    log_warning "即将重启服务，这将导致短暂的服务中断（约 2-3 秒）"
    if ! confirm "是否继续？" "y"; then
        log_info "部署已取消"
        exit 0
    fi
    
    log_info "替换二进制文件..."
    remote_exec "cd $DEPLOY_DIR && mv ${BINARY_NAME}.new $BINARY_NAME"
    
    log_info "重启服务..."
    remote_exec "systemctl restart $SERVICE_NAME"
    
    log_info "等待服务启动（3 秒）..."
    sleep 3
    
    # 检查服务状态
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "服务启动成功"
    else
        log_error "服务启动失败！"
        log_error "查看日志: journalctl -u $SERVICE_NAME -n 50"
        
        if confirm "是否自动回滚？" "y"; then
            rollback_deployment
        fi
        exit 1
    fi
    
    echo ""
}

# ==================== 部署后验证 ====================

post_deployment_verification() {
    log_info "=========================================="
    log_info "部署后验证"
    log_info "=========================================="
    
    # 1. 版本检查
    log_info "1. 检查服务版本..."
    version_output=$(curl -s "http://${PROD_HOST}:${API_PORT}/api/system/version" || echo "")
    if [[ -n "$version_output" ]]; then
        log_success "版本检查通过"
        echo "$version_output" | jq . 2>/dev/null || echo "$version_output"
    else
        log_error "无法获取版本信息"
    fi
    echo ""
    
    # 2. 健康检查
    log_info "2. 健康检查..."
    health_output=$(curl -s "http://${PROD_HOST}:${API_PORT}/api/system/health" || echo "")
    if [[ -n "$health_output" ]] && echo "$health_output" | grep -q "healthy"; then
        log_success "健康检查通过"
        echo "$health_output" | jq . 2>/dev/null || echo "$health_output"
    else
        log_warning "健康检查未通过"
        echo "$health_output"
    fi
    echo ""
    
    # 3. 检查启动日志
    log_info "3. 检查启动日志（最近 20 行）..."
    remote_exec "journalctl -u $SERVICE_NAME --since '1 minute ago' -n 20 --no-pager" || true
    echo ""
    
    # 4. 检查错误日志
    log_info "4. 检查错误日志..."
    error_count=$(remote_exec "journalctl -u $SERVICE_NAME --since '1 minute ago' --no-pager | grep -c -E '(ERROR|FATAL)' || true")
    if [[ $error_count -eq 0 ]]; then
        log_success "无错误日志"
    else
        log_warning "发现 $error_count 条错误日志"
        remote_exec "journalctl -u $SERVICE_NAME --since '1 minute ago' --no-pager | grep -E '(ERROR|FATAL)'" || true
    fi
    echo ""
    
    # 5. 数据库查询验证
    log_info "5. 验证数据库连接和最近请求..."
    remote_exec "PGPASSWORD='$DB_PASS' psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME" <<'EOF'
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE success = true) AS success_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / NULLIF(COUNT(*), 0), 2) AS success_rate
FROM request_logs_hot
WHERE ts >= NOW() - INTERVAL '5 minutes';
EOF
    echo ""
    
    log_success "部署后验证完成"
}

# ==================== 回滚部署 ====================

rollback_deployment() {
    log_warning "=========================================="
    log_warning "开始回滚部署"
    log_warning "=========================================="
    
    # 获取备份文件名
    local backup_name
    if [[ -f /tmp/llmgo_backup_name.txt ]]; then
        backup_name=$(cat /tmp/llmgo_backup_name.txt)
    else
        # 查找最新的备份
        backup_name=$(remote_exec "cd $DEPLOY_DIR && ls -t ${BINARY_NAME}.backup.* 2>/dev/null | head -1" || echo "")
    fi
    
    if [[ -z "$backup_name" ]]; then
        log_error "未找到备份文件，无法回滚"
        exit 1
    fi
    
    log_info "使用备份: $backup_name"
    
    # 停止服务
    log_info "停止服务..."
    remote_exec "systemctl stop $SERVICE_NAME"
    
    # 保存失败的版本
    log_info "保存失败的版本..."
    remote_exec "cd $DEPLOY_DIR && mv $BINARY_NAME ${BINARY_NAME}.failed.$(date +%Y%m%d_%H%M%S)"
    
    # 恢复备份
    log_info "恢复备份..."
    remote_exec "cd $DEPLOY_DIR && cp $backup_name $BINARY_NAME"
    
    # 启动服务
    log_info "启动服务..."
    remote_exec "systemctl start $SERVICE_NAME"
    
    sleep 3
    
    # 验证回滚
    if remote_exec "systemctl is-active $SERVICE_NAME" &>/dev/null; then
        log_success "回滚成功，服务已恢复"
    else
        log_error "回滚后服务仍未正常启动"
        log_error "请手动检查: journalctl -u $SERVICE_NAME -n 50"
        exit 1
    fi
}

# ==================== 监控指引 ====================

print_monitoring_guide() {
    log_info "=========================================="
    log_info "后续监控指引"
    log_info "=========================================="
    
    echo ""
    echo "请在接下来的 30 分钟内持续监控以下指标："
    echo ""
    echo "1. 实时日志监控:"
    echo "   ssh -p $PROD_PORT $PROD_USER@$PROD_HOST \"journalctl -u $SERVICE_NAME -f\""
    echo ""
    echo "2. 健康检查脚本:"
    echo "   ./scripts/health-check-154.sh"
    echo ""
    echo "3. Prometheus 指标:"
    echo "   - llm_gateway_malformed_sse_frame_total（预期为 0）"
    echo "   - 请求成功率（预期 > 85%）"
    echo ""
    echo "4. 数据库查询（最近 10 分钟统计）:"
    echo "   ssh -p $PROD_PORT $PROD_USER@$PROD_HOST 'PGPASSWORD=\"$DB_PASS\" psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c \"SELECT COUNT(*) FILTER (WHERE success = true) * 100.0 / COUNT(*) AS success_rate FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '\"'\"'10 minutes'\"'\"';\"'"
    echo ""
    echo "如需回滚，运行:"
    echo "   ./scripts/rollback-154.sh"
    echo ""
}

# ==================== 主流程 ====================

main() {
    local start_time=$(date +%s)
    
    log_info "=========================================="
    log_info "154 生产环境部署脚本"
    log_info "功能: SSE Frame Validation"
    log_info "时间: $(date '+%Y-%m-%d %H:%M:%S')"
    log_info "=========================================="
    echo ""
    
    # Dry-run 模式
    if [[ "${DRY_RUN:-false}" == "true" ]]; then
        log_warning "DRY-RUN 模式: 仅执行检查，不实际部署"
        pre_deployment_checks
        log_info "DRY-RUN 完成"
        exit 0
    fi
    
    # 确认部署
    echo "目标环境: $PROD_USER@$PROD_HOST:$PROD_PORT"
    echo "服务名称: $SERVICE_NAME"
    echo "部署目录: $DEPLOY_DIR"
    echo "本地文件: $LOCAL_BINARY"
    echo ""
    
    if ! confirm "确认开始部署？"; then
        log_info "部署已取消"
        exit 0
    fi
    echo ""
    
    # 执行部署流程
    pre_deployment_checks
    backup_current_version
    upload_new_version
    deploy_new_version
    post_deployment_verification
    
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    echo ""
    log_success "=========================================="
    log_success "部署完成！"
    log_success "总耗时: ${duration} 秒"
    log_success "=========================================="
    echo ""
    
    print_monitoring_guide
}

# ==================== 脚本入口 ====================

# 捕获错误
trap 'log_error "脚本执行失败，退出码: $?"' ERR

# 执行主流程
main "$@"
