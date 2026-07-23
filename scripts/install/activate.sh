#!/usr/bin/env bash
# scripts/install/activate.sh - 激活工具
# 用法：sudo ./activate.sh <license_file>

set -euo pipefail

INSTALL_DIR="/opt/llm-gateway-go"
SERVICE_NAME="llm-gateway-go"

LICENSE_FILE="${1:?Usage: $0 <license_file>}"

if [[ ! -f "$LICENSE_FILE" ]]; then
    echo "❌ 文件不存在: $LICENSE_FILE"
    exit 1
fi

echo "========================================="
echo "LLM Gateway Go 激活工具"
echo "========================================="
echo ""

# 检查服务状态
echo "📋 检查服务..."
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    echo "   ✅ 服务运行中"
else
    echo "   ⚠️  服务未运行"
    read -p "   启动服务？[y/n] " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy] ]] && systemctl start "$SERVICE_NAME"
fi

# 导入许可证
echo ""
echo "🔑 导入许可证..."
TARGET="${INSTALL_DIR}/license.dat"
cp "$LICENSE_FILE" "$TARGET"
chown "$SERVICE_USER:$SERVICE_USER" "$TARGET" 2>/dev/null || true
chmod 600 "$TARGET"

echo "   ✅ 许可证已导入: $TARGET"

# 重启服务使激活生效
echo ""
echo "🔄 重启服务..."
systemctl restart "$SERVICE_NAME"
sleep 5

if systemctl is-active --quiet "$SERVICE_NAME"; then
    echo "   ✅ 服务已重启"
else
    echo "   ❌ 服务重启失败"
    exit 1
fi

# 验证激活
echo ""
echo "✅ 验证激活..."
LICENSE_INFO=$(curl -fsS http://localhost:8781/api/system/license 2>/dev/null || echo "")

if [[ -n "$LICENSE_INFO" ]]; then
    echo "$LICENSE_INFO" | jq '.'
    echo ""
    echo "🎉 激活成功！"
else
    echo "   ⚠️  无法获取许可证信息"
    echo "   查看日志: journalctl -u $SERVICE_NAME -n 30"
fi

