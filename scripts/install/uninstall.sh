#!/usr/bin/env bash
# scripts/install/uninstall.sh - 卸载工具
# 用法：sudo ./uninstall.sh [--keep-data | --purge]

set -euo pipefail

INSTALL_DIR="/opt/llm-gateway-go"
SERVICE_USER="llm-gateway"
SERVICE_NAME="llm-gateway-go"
DATA_DIR="/var/lib/llm-gateway-go"
LOG_DIR="/var/log/llm-gateway-go"
CONFIG_DIR="/etc/llm-gateway-go"

KEEP_DATA=false

for arg in "$@"; do
    case "$arg" in
        --keep-data)
            KEEP_DATA=true
            ;;
        --purge)
            KEEP_DATA=false
            ;;
        --help|-h)
            cat << HELP
用法: sudo $0 [选项]

选项:
  --keep-data    保留数据目录（默认）
  --purge        完全删除包括数据
  --help, -h     显示帮助
HELP
            exit 0
            ;;
    esac
done

if [[ $EUID -ne 0 ]]; then
    echo "❌ 需要 root 权限"
    exit 1
fi

echo "========================================="
echo "LLM Gateway Go 卸载工具"
echo "========================================="
echo ""

read -p "确认卸载？[yes/no] " -r
echo
if [[ ! $REPLY =~ ^[Yy]es$ ]]; then
    echo "已取消"
    exit 0
fi

# 停止服务
echo "🛑 停止服务..."
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    systemctl stop "$SERVICE_NAME"
    sleep 2
    echo "   ✅ 服务已停止"
else
    echo "   ℹ️  服务未运行"
fi

# 禁用服务
echo ""
echo "🚫 禁用服务..."
systemctl disable "$SERVICE_NAME" 2>/dev/null || true

# 删除systemd文件
echo ""
echo "🗑️  删除服务文件..."
if [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]]; then
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    echo "   ✅ systemd 文件已删除"
fi

# 删除安装目录
echo ""
echo "🗑️  删除安装目录..."
if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    echo "   ✅ $INSTALL_DIR"
fi

# 删除配置
echo ""
echo "🗑️  删除配置..."
[[ -d "$CONFIG_DIR" ]] && rm -rf "$CONFIG_DIR" && echo "   ✅ $CONFIG_DIR"

# 删除日志
echo ""
echo "🗑️  删除日志..."
[[ -d "$LOG_DIR" ]] && rm -rf "$LOG_DIR" && echo "   ✅ $LOG_DIR"

# 删除数据
if [[ "$KEEP_DATA" == "false" ]]; then
    echo ""
    echo "🗑️  删除数据..."
    if [[ -d "$DATA_DIR" ]]; then
        rm -rf "$DATA_DIR"
        echo "   ✅ $DATA_DIR"
    fi
else
    echo ""
    echo "ℹ️  保留数据目录: $DATA_DIR"
fi

# 删除用户
echo ""
echo "👤 删除用户..."
if id "$SERVICE_USER" &>/dev/null; then
    userdel "$SERVICE_USER" 2>/dev/null || true
    echo "   ✅ 用户已删除"
fi

echo ""
echo "========================================="
echo "✅ 卸载完成"
echo "========================================="
[[ "$KEEP_DATA" == "true" ]] && echo "数据已保留在: $DATA_DIR"

