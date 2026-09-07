#!/usr/bin/env bash
# scripts/deploy/rollback.sh - 回滚脚本
# 用法：bash rollback.sh <environment> [backup_path]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================================
# 配置
# ============================================================================

ENV="${1:?Usage: $0 <environment> [backup_path]}"
BACKUP_PATH="${2:-}"

case "$ENV" in
    "245")
        SERVER_HOST="root@8.136.114.245"
        SERVER_PORT="25022"
        REMOTE_DEPLOY_DIR="/opt/llm-gateway-go"
        REMOTE_BACKUP_DIR="/opt/backups/llm-gateway-go"
        ;;
    "local")
        # 本地环境
        REMOTE_DEPLOY_DIR="/opt/llm-gateway-go"
        REMOTE_BACKUP_DIR="/opt/backups/llm-gateway-go"
        ;;
    *)
        echo "❌ 未知环境: $ENV"
        echo "支持的环境: 245, local"
        exit 1
        ;;
esac

echo "========================================="
echo "回滚: $ENV"
echo "========================================="

# ============================================================================
# SSH 辅助函数
# ============================================================================

if [[ "$ENV" == "245" ]]; then
    ssh_exec() {
        ssh -p "$SERVER_PORT" "$SERVER_HOST" "$@"
    }
    
    scp_download() {
        scp -P "$SERVER_PORT" "${SERVER_HOST}:$1" "$2"
    }
else
    ssh_exec() {
        bash -c "$@"
    }
fi

# ============================================================================
# 查找最新备份
# ============================================================================

find_latest_backup() {
    echo ""
    echo "🔍 查找最新备份..."
    
    if [[ -n "$BACKUP_PATH" ]]; then
        echo "   使用指定备份: $BACKUP_PATH"
        return 0
    fi
    
    # 查找最新备份
    LATEST_BACKUP=$(ssh_exec "ls -t $REMOTE_BACKUP_DIR | head -1")
    
    if [[ -z "$LATEST_BACKUP" ]]; then
        echo "   ❌ 未找到备份"
        exit 1
    fi
    
    BACKUP_PATH="${REMOTE_BACKUP_DIR}/${LATEST_BACKUP}"
    echo "   ✅ 找到备份: $BACKUP_PATH"
    
    # 显示备份信息
    if ssh_exec "test -f $BACKUP_PATH/backup_info.txt"; then
        echo ""
        echo "备份信息:"
        ssh_exec "cat $BACKUP_PATH/backup_info.txt" | sed 's/^/   /'
    fi
    
    echo ""
    read -p "确认回滚到此备份? (yes/no) " -r
    if [[ ! $REPLY =~ ^[Yy]es$ ]]; then
        echo "已取消回滚"
        exit 0
    fi
}

# ============================================================================
# 停止服务
# ============================================================================

stop_service() {
    echo ""
    echo "🛑 停止服务..."
    
    if ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
        ssh_exec "systemctl stop llm-gateway-go"
        
        # 等待服务停止
        for i in {1..10}; do
            if ! ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
                echo "   ✅ 服务已停止"
                return 0
            fi
            sleep 1
        done
        
        echo "   ❌ 服务停止超时"
        exit 1
    else
        echo "   ℹ️  服务未运行"
    fi
}

# ============================================================================
# 恢复备份
# ============================================================================

restore_backup() {
    echo ""
    echo "📦 恢复备份..."
    
    # 恢复二进制
    if ssh_exec "test -f $BACKUP_PATH/llm-gateway-go"; then
        echo "   恢复二进制文件..."
        ssh_exec "cp -p $BACKUP_PATH/llm-gateway-go $REMOTE_DEPLOY_DIR/bin/"
        ssh_exec "chmod +x $REMOTE_DEPLOY_DIR/bin/llm-gateway-go"
        echo "   ✅ 二进制已恢复"
    else
        echo "   ⚠️  备份中无二进制文件"
    fi
    
    # 恢复配置
    if ssh_exec "test -f $BACKUP_PATH/config.yaml"; then
        echo "   恢复配置文件..."
        ssh_exec "cp -p $BACKUP_PATH/config.yaml $REMOTE_DEPLOY_DIR/config/"
        echo "   ✅ 配置已恢复"
    else
        echo "   ⚠️  备份中无配置文件"
    fi
    
    # 恢复数据库（可选）
    if ssh_exec "test -f $BACKUP_PATH/db_backup.sql"; then
        echo ""
        read -p "是否恢复数据库? (yes/no) " -r
        if [[ $REPLY =~ ^[Yy]es$ ]]; then
            echo "   恢复数据库..."
            # 2026-09-07: 同 deploy-to-245.sh 备注，245/252/本地统一约定
            # 唯一登录角色是 llm_gateway；走 LLM_GATEWAY_PG_USER 模板。
            ssh_exec "PGPASSWORD=\"\${DB_PASS:-}\" psql -h localhost -U \"\${LLM_GATEWAY_PG_USER:-llm_gateway}\" llm_gateway < $BACKUP_PATH/db_backup.sql"
            echo "   ✅ 数据库已恢复"
        else
            echo "   ⏭️  跳过数据库恢复"
        fi
    fi
    
    echo "   ✅ 备份恢复完成"
}

# ============================================================================
# 启动服务
# ============================================================================

start_service() {
    echo ""
    echo "🚀 启动服务..."
    
    ssh_exec "systemctl start llm-gateway-go"
    
    # 等待服务启动
    sleep 5
    
    if ssh_exec "systemctl is-active llm-gateway-go" &>/dev/null; then
        echo "   ✅ 服务已启动"
    else
        echo "   ❌ 服务启动失败"
        echo "   查看日志: journalctl -u llm-gateway-go -n 50"
        exit 1
    fi
}

# ============================================================================
# 健康检查
# ============================================================================

verify_rollback() {
    echo ""
    echo "🏥 健康检查..."
    
    case "$ENV" in
        "245")
            bash "${SCRIPT_DIR}/health-check.sh" "http://8.136.114.245:8781"
            ;;
        "local")
            bash "${SCRIPT_DIR}/health-check.sh" "http://localhost:8781"
            ;;
    esac
    
    if [[ $? -eq 0 ]]; then
        echo "   ✅ 健康检查通过"
    else
        echo "   ❌ 健康检查失败"
        echo "   回滚后服务仍异常，需要人工介入"
        exit 1
    fi
}

# ============================================================================
# 记录回滚
# ============================================================================

record_rollback() {
    echo ""
    echo "📝 记录回滚信息..."
    
    ROLLBACK_RECORD="/tmp/rollback_record.txt"
    cat > "$ROLLBACK_RECORD" << INFO
回滚时间: $(date)
环境: $ENV
备份路径: $BACKUP_PATH
操作者: $(whoami)@$(hostname)
原因: 部署失败自动回滚
INFO
    
    if [[ "$ENV" == "245" ]]; then
        scp -P "$SERVER_PORT" "$ROLLBACK_RECORD" \
            "${SERVER_HOST}:${REMOTE_DEPLOY_DIR}/logs/rollback-$(date +%Y%m%d-%H%M%S).txt"
    else
        cp "$ROLLBACK_RECORD" "${REMOTE_DEPLOY_DIR}/logs/rollback-$(date +%Y%m%d-%H%M%S).txt"
    fi
    
    rm "$ROLLBACK_RECORD"
    echo "   ✅ 回滚信息已记录"
}

# ============================================================================
# 主流程
# ============================================================================

main() {
    find_latest_backup
    stop_service
    restore_backup
    start_service
    verify_rollback
    record_rollback
    
    echo ""
    echo "========================================="
    echo "✅ 回滚完成"
    echo "========================================="
    echo ""
    echo "备份路径: $BACKUP_PATH"
    echo "服务状态: 运行中"
    echo ""
}

# 执行主流程
main

