#!/usr/bin/env bash
# =====================================================================
# scripts/ab-test-pressure.sh - 压力感知路由 A/B 测试自动化脚本
#
# 用法:
#   bash scripts/ab-test-pressure.sh enable        # 启用 Feature flag
#   bash scripts/ab-test-pressure.sh disable       # 禁用 Feature flag
#   bash scripts/ab-test-pressure.sh status        # 查看当前状态
#   bash scripts/ab-test-pressure.sh metrics       # 查看关键指标
#   bash scripts/ab-test-pressure.sh rollback      # 一键回滚（关闭 flag）
#
# 服务器：245 (8.136.114.245)
# =====================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER="root@8.136.114.245"
SERVICE="llm-gateway"
METRICS_PORT=8781
FEATURE_FLAG="PRESSURE_AWARE_ROUTING"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_ok()      { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_err()     { echo -e "${RED}[ERR]${NC} $1"; }

# ----------------------------------------------------------------------------
# 启用 Feature flag
# ----------------------------------------------------------------------------
enable_feature() {
    log_info "启用压力感知路由 Feature flag..."

    ssh "$SERVER" "
        set -e
        # 1. 备份 .env
        cp /opt/llm-gateway-go/.env /opt/llm-gateway-go/.env.bak.ab-test.\$(date +%Y%m%d-%H%M%S)

        # 2. 移除可能存在的旧设置
        sed -i '/^${FEATURE_FLAG}=/d' /opt/llm-gateway-go/.env

        # 3. 添加新的设置
        echo '${FEATURE_FLAG}=true' >> /opt/llm-gateway-go/.env

        # 4. 重启服务
        systemctl restart $SERVICE

        # 5. 等待服务启动
        sleep 5
    "

    log_ok "Feature flag 已启用"

    # 验证
    log_info "验证启用成功..."
    if status_feature | grep -q "Feature flag 状态.*active\|启用"; then
        log_ok "启用成功，可以开始 A/B 测试"
    else
        log_warn "请手动检查服务状态"
    fi
}

# ----------------------------------------------------------------------------
# 禁用 Feature flag
# ----------------------------------------------------------------------------
disable_feature() {
    log_info "禁用压力感知路由 Feature flag..."

    ssh "$SERVER" "
        set -e
        # 1. 移除设置（如果存在）
        sed -i '/^${FEATURE_FLAG}=/d' /opt/llm-gateway-go/.env

        # 2. 重启服务
        systemctl restart $SERVICE

        # 3. 等待服务启动
        sleep 5
    "

    log_ok "Feature flag 已禁用"
}

# ----------------------------------------------------------------------------
# 查看状态
# ----------------------------------------------------------------------------
status_feature() {
    log_info "查看压力感知路由状态..."

    ssh "$SERVER" "
        echo '==================== 服务状态 ===================='
        systemctl is-active $SERVICE || echo '服务未运行'
        echo ''
        echo '==================== .env 配置 ===================='
        grep -E '^(${FEATURE_FLAG}|URSM_V2_MODE)' /opt/llm-gateway-go/.env || echo '未设置相关环境变量'
        echo ''
        echo '==================== 启动日志 ===================='
        journalctl -u $SERVICE -n 100 --no-pager | grep -E 'pressure|URSM' | tail -10 || echo '无相关日志'
    "
}

# ----------------------------------------------------------------------------
# 查看指标
# ----------------------------------------------------------------------------
metrics_feature() {
    log_info "采集压力感知路由 Prometheus 指标..."

    METRICS=$(curl -s --max-time 10 "http://$SERVER_HOSTNAME:8781/metrics" 2>/dev/null || echo "")

    if [ -z "$METRICS" ]; then
        # 尝试 SSH 内部访问
        log_info "尝试通过 SSH 访问 Prometheus 指标..."
        METRICS=$(ssh "$SERVER" "curl -s --max-time 10 http://localhost:8781/metrics" 2>/dev/null || echo "")
    fi

    if [ -z "$METRICS" ]; then
        log_warn "无法获取 Prometheus 指标"
        return 1
    fi

    log_ok "Prometheus 关键指标："
    echo ""
    echo "=========================================="
    echo "$METRICS" | grep -E "^llmgw_pressure|^llmgw_weight_adjustment" | head -30
    echo "=========================================="
}

# ----------------------------------------------------------------------------
# 一键回滚（关闭 flag + 重启）
# ----------------------------------------------------------------------------
rollback_feature() {
    log_warn "一键回滚压力感知路由..."

    ssh "$SERVER" "
        set -e
        # 1. 确保 flag 已禁用
        sed -i '/^${FEATURE_FLAG}=/d' /opt/llm-gateway-go/.env

        # 2. 重启服务
        systemctl restart $SERVICE

        # 3. 等待
        sleep 5
    "

    log_ok "回滚完成，Feature flag 已禁用"
}

# ----------------------------------------------------------------------------
# 帮助
# ----------------------------------------------------------------------------
usage() {
    cat << EOF
用法: $0 <命令>

命令:
  enable     启用 Feature flag (开始 A/B 测试)
  disable    禁用 Feature flag (停止 A/B 测试)
  status     查看当前状态
  metrics    查看 Prometheus 指标
  rollback   一键回滚（禁用 flag）

示例:
  $0 enable
  $0 status
  $0 metrics
  $0 disable
EOF
}

# ----------------------------------------------------------------------------
# 主入口
# ----------------------------------------------------------------------------
case "${1:-}" in
    enable)
        enable_feature
        ;;
    disable)
        disable_feature
        ;;
    status)
        status_feature
        ;;
    metrics)
        metrics_feature
        ;;
    rollback)
        rollback_feature
        ;;
    *)
        usage
        exit 1
        ;;
esac
