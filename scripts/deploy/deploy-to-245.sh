#!/usr/bin/env bash
# scripts/deploy/deploy-to-245.sh - 部署到 245 预生产环境
# 用法：bash deploy-to-245.sh <archive_file>

set -euo pipefail

# ============================================================================
# 配置
# ============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# 245 服务器配置
SERVER_HOST="root@8.136.114.245"
SERVER_PORT="25022"
REMOTE_DEPLOY_DIR="/opt/llm-gateway-go"
REMOTE_BACKUP_DIR="/opt/backups/llm-gateway-go"

# 获取参数
ARCHIVE_FILE="${1:?Usage: $0 <archive_file>}"

if [[ ! -f "$ARCHIVE_FILE" ]]; then
    echo "❌ 文件不存在: $ARCHIVE_FILE"
    exit 1
fi

# 日志
DEPLOY_LOG="${PROJECT_ROOT}/build/logs/deploy-245-$(date +%Y%m%d-%H%M%S).log"
mkdir -p "$(dirname "$DEPLOY_LOG")"

# ============================================================================
# 日志函数
# ============================================================================

log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" | tee -a "$DEPLOY_LOG"
}

log_success() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ✅ $*" | tee -a "$DEPLOY_LOG"
}

log_error() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ❌ $*" | tee -a "$DEPLOY_LOG"
}

log_step() {
    echo ""
    echo "=========================================" | tee -a "$DEPLOY_LOG"
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] 🚀 $*" | tee -a "$DEPLOY_LOG"
    echo "=========================================" | tee -a "$DEPLOY_LOG"
}

# ============================================================================
# SSH 辅助函数
# ============================================================================

ssh_exec() {
    ssh -p "$SERVER_PORT" "$SERVER_HOST" "$@"
}

scp_upload() {
    scp -P "$SERVER_PORT" "$@"
}

# ============================================================================
# 步骤 1: 预检查
# ============================================================================

pre_check() {
    log_step "步骤 1: 预检查"
    
    # 检查 SSH 连接
    log "检查 SSH 连接..."
    if ! ssh_exec "echo 'SSH connection OK'" &>/dev/null; then
        log_error "无法连接到 245 服务器"
        log_error "请检查 SSH 配置: $SERVER_HOST:$SERVER_PORT"
        exit 1
    fi
    log_success "SSH 连接正常"
    
    # 检查磁盘空间
    log "检查磁盘空间..."
    AVAILABLE_GB=$(ssh_exec "df / | tail -1 | awk '{print int(\$4/1024/1024)}'")
    log "可用空间: ${AVAILABLE_GB}GB"
    
    if [[ $AVAILABLE_GB -lt 5 ]]; then
        log_error "磁盘空间不足（< 5GB）"
        exit 1
    fi
    
    # 检查当前运行状态
    log "检查当前服务状态..."
    if ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
        CURRENT_VERSION=$(ssh_exec "curl -fsS http://localhost:8781/api/system/version 2>/dev/null | jq -r '.version' || echo 'unknown'")
        log "当前版本: $CURRENT_VERSION"
    else
        log "服务未运行"
    fi
    
    log_success "预检查完成"
}

# ============================================================================
# 步骤 2: 备份当前版本
# ============================================================================

backup_current() {
    log_step "步骤 2: 备份当前版本"
    
    # 创建备份目录
    BACKUP_TIME=$(date +%Y%m%d-%H%M%S)
    BACKUP_PATH="${REMOTE_BACKUP_DIR}/${BACKUP_TIME}"
    
    ssh_exec "mkdir -p $BACKUP_PATH"
    
    # 备份二进制
    log "备份二进制文件..."
    ssh_exec "if [[ -f $REMOTE_DEPLOY_DIR/bin/llm-gateway-go ]]; then \
        cp -p $REMOTE_DEPLOY_DIR/bin/llm-gateway-go $BACKUP_PATH/; \
    fi"
    
    # 备份配置
    log "备份配置文件..."
    ssh_exec "if [[ -f $REMOTE_DEPLOY_DIR/config/config.yaml ]]; then \
        cp -p $REMOTE_DEPLOY_DIR/config/config.yaml $BACKUP_PATH/; \
    fi"
    
    # 备份数据库（可选）
    log "备份数据库..."
    ssh_exec "if command -v pg_dump &>/dev/null; then \
        pg_dump -h localhost -U postgres llm_gateway > $BACKUP_PATH/db_backup.sql 2>/dev/null || true; \
    fi"
    
    # 记录备份信息
    cat > /tmp/backup_info.txt << INFO
备份时间: $(date)
备份路径: $BACKUP_PATH
原因: 部署新版本
归档文件: $(basename "$ARCHIVE_FILE")
INFO
    
    scp_upload /tmp/backup_info.txt "${SERVER_HOST}:${BACKUP_PATH}/backup_info.txt"
    rm /tmp/backup_info.txt
    
    log_success "备份完成: $BACKUP_PATH"
    
    # 导出备份路径供回滚使用
    echo "$BACKUP_PATH" > /tmp/last_backup_path.txt
}

# ============================================================================
# 步骤 3: 上传新版本
# ============================================================================

upload_package() {
    log_step "步骤 3: 上传新版本"
    
    REMOTE_TEMP_DIR="/tmp/llm-gateway-deploy-$(date +%s)"
    ssh_exec "mkdir -p $REMOTE_TEMP_DIR"
    
    log "上传文件: $(basename "$ARCHIVE_FILE")"
    scp_upload "$ARCHIVE_FILE" "${SERVER_HOST}:${REMOTE_TEMP_DIR}/"
    
    log "解压文件..."
    ARCHIVE_NAME=$(basename "$ARCHIVE_FILE")
    ssh_exec "cd $REMOTE_TEMP_DIR && tar xzf $ARCHIVE_NAME"
    
    # 获取解压后的目录名
    EXTRACTED_DIR=$(ssh_exec "cd $REMOTE_TEMP_DIR && ls -d */ | head -1 | tr -d '/'")
    
    log_success "文件已上传到: ${REMOTE_TEMP_DIR}/${EXTRACTED_DIR}"
    
    # 导出路径供后续使用
    echo "${REMOTE_TEMP_DIR}/${EXTRACTED_DIR}" > /tmp/deploy_temp_dir.txt
}

# ============================================================================
# 步骤 4: 停止服务
# ============================================================================

stop_service() {
    log_step "步骤 4: 停止服务"
    
    if ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
        log "停止服务..."
        ssh_exec "systemctl stop llm-gateway-go"
        
        # 等待服务完全停止
        for i in {1..10}; do
            if ! ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
                log_success "服务已停止"
                return 0
            fi
            sleep 1
        done
        
        log_error "服务停止超时"
        exit 1
    else
        log "服务未运行，跳过停止"
    fi
}

# ============================================================================
# 步骤 5: 安装新版本
# ============================================================================

install_new_version() {
    log_step "步骤 5: 安装新版本"
    
    DEPLOY_TEMP_DIR=$(cat /tmp/deploy_temp_dir.txt)
    
    # 创建目录结构
    log "创建目录结构..."
    ssh_exec "mkdir -p $REMOTE_DEPLOY_DIR/{bin,web,config,logs}"
    
    # 安装二进制
    log "安装二进制文件..."
    ssh_exec "cp $DEPLOY_TEMP_DIR/bin/llm-gateway-go $REMOTE_DEPLOY_DIR/bin/"
    ssh_exec "chmod +x $REMOTE_DEPLOY_DIR/bin/llm-gateway-go"
    
    # 安装前端
    log "安装前端文件..."
    ssh_exec "rm -rf $REMOTE_DEPLOY_DIR/web/dist"
    ssh_exec "cp -r $DEPLOY_TEMP_DIR/web/dist $REMOTE_DEPLOY_DIR/web/"
    
    # 更新配置（如果不存在）
    log "检查配置文件..."
    ssh_exec "if [[ ! -f $REMOTE_DEPLOY_DIR/config/config.yaml ]]; then \
        cp $DEPLOY_TEMP_DIR/config/config.yaml.example $REMOTE_DEPLOY_DIR/config/config.yaml; \
    fi"
    
    # 设置权限
    log "设置权限..."
    ssh_exec "chown -R llm-gateway:llm-gateway $REMOTE_DEPLOY_DIR 2>/dev/null || true"
    
    # 清理临时文件
    log "清理临时文件..."
    ssh_exec "rm -rf $DEPLOY_TEMP_DIR"
    
    log_success "新版本已安装"
}

# ============================================================================
# 步骤 6: 启动服务
# ============================================================================

start_service() {
    log_step "步骤 6: 启动服务"
    
    log "启动服务..."
    ssh_exec "systemctl start llm-gateway-go"
    
    # 等待服务启动
    sleep 5
    
    # 检查服务状态
    if ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
        log_success "服务已启动"
    else
        log_error "服务启动失败"
        log "查看日志: ssh -p $SERVER_PORT $SERVER_HOST 'journalctl -u llm-gateway-go -n 50'"
        exit 1
    fi
}

# ============================================================================
# 步骤 7: 健康检查
# ============================================================================

health_check() {
    log_step "步骤 7: 健康检查"
    
    bash "${SCRIPT_DIR}/health-check.sh" "http://8.136.114.245:8781"
    
    if [[ $? -eq 0 ]]; then
        log_success "健康检查通过"
    else
        log_error "健康检查失败"
        
        # 自动回滚
        log "触发自动回滚..."
        bash "${SCRIPT_DIR}/rollback.sh" "245"
        exit 1
    fi
}

# ============================================================================
# 步骤 8: 验证部署
# ============================================================================

verify_deployment() {
    log_step "步骤 8: 验证部署"
    
    # 检查版本
    log "检查版本信息..."
    NEW_VERSION=$(ssh_exec "curl -fsS http://localhost:8781/api/system/version 2>/dev/null | jq -r '.version' || echo 'unknown'")
    log "新版本: $NEW_VERSION"
    
    # 检查数据库连接
    log "检查数据库连接..."
    DB_STATUS=$(ssh_exec "curl -fsS http://localhost:8781/api/internal/ready/db 2>/dev/null || echo 'FAIL'")
    if [[ "$DB_STATUS" == "OK" ]]; then
        log_success "数据库连接正常"
    else
        log_error "数据库连接失败"
        exit 1
    fi
    
    # 检查Redis连接
    log "检查 Redis 连接..."
    REDIS_STATUS=$(ssh_exec "curl -fsS http://localhost:8781/api/internal/ready/redis 2>/dev/null || echo 'FAIL'")
    if [[ "$REDIS_STATUS" == "OK" ]]; then
        log_success "Redis 连接正常"
    else
        log_error "Redis 连接失败"
        exit 1
    fi
    
    log_success "部署验证完成"
}

# ============================================================================
# 步骤 9: 记录部署信息
# ============================================================================

record_deployment() {
    log_step "步骤 9: 记录部署信息"
    
    cat > /tmp/deployment_record.json << JSON
{
  "deployed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "archive": "$(basename "$ARCHIVE_FILE")",
  "server": "245",
  "backup_path": "$(cat /tmp/last_backup_path.txt 2>/dev/null || echo 'none')",
  "new_version": "$NEW_VERSION",
  "deployed_by": "$(whoami)@$(hostname)",
  "status": "success"
}
JSON
    
    scp_upload /tmp/deployment_record.json "${SERVER_HOST}:${REMOTE_DEPLOY_DIR}/logs/deployment-$(date +%Y%m%d-%H%M%S).json"
    rm /tmp/deployment_record.json
    
    log_success "部署信息已记录"
}

# ============================================================================
# 主流程
# ============================================================================

main() {
    log_step "开始部署到 245"
    
    pre_check
    backup_current
    upload_package
    stop_service
    install_new_version
    start_service
    health_check
    verify_deployment
    record_deployment
    
    log_step "部署完成"
    log_success "新版本: $NEW_VERSION"
    log_success "服务地址: http://8.136.114.245:8781"
    log_success "日志文件: $DEPLOY_LOG"
    
    echo ""
    echo "🎉 部署成功!"
    echo "   版本: $NEW_VERSION"
    echo "   服务: http://8.136.114.245:8781"
    echo ""
    echo "验证命令:"
    echo "   curl http://8.136.114.245:8781/healthz"
    echo "   curl http://8.136.114.245:8781/api/system/version"
    echo ""
}

# 错误处理
trap 'log_error "部署失败，查看日志: $DEPLOY_LOG"; exit 1' ERR

# 执行主流程
main

