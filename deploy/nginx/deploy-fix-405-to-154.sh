#!/bin/bash
# 部署 405 修复到 llm.kxpms.cn (154 服务器)
# 使用方法: ./deploy-fix-405-to-154.sh

set -euo pipefail

SERVER="47.97.111.154"
USER="root"
CONFIG_SOURCE="deploy/nginx/active-20260821/llm-kxpms-cn-154.conf.20260821-spa-fallback-only"
CONFIG_TARGET="/etc/nginx/conf.d/llm.kxpms.cn.conf"

echo "=========================================="
echo "部署 llm.kxpms.cn nginx 配置修复 (405)"
echo "=========================================="
echo "服务器: $SERVER"
echo "源文件: $CONFIG_SOURCE"
echo "目标文件: $CONFIG_TARGET"
echo ""

# 1. 检查源文件是否存在
if [ ! -f "$CONFIG_SOURCE" ]; then
    echo "❌ 错误: 源配置文件不存在: $CONFIG_SOURCE"
    exit 1
fi

echo "✓ 源配置文件存在"

# 2. 备份远程配置
echo ""
echo "步骤 1/5: 备份远程配置..."
BACKUP_NAME="llm.kxpms.cn.conf.backup-$(date +%Y%m%d-%H%M%S)"
ssh ${USER}@${SERVER} "cp $CONFIG_TARGET /etc/nginx/conf.d/$BACKUP_NAME" || {
    echo "❌ 备份失败"
    exit 1
}
echo "✓ 备份成功: $BACKUP_NAME"

# 3. 上传新配置
echo ""
echo "步骤 2/5: 上传新配置..."
scp "$CONFIG_SOURCE" "${USER}@${SERVER}:${CONFIG_TARGET}" || {
    echo "❌ 上传失败"
    exit 1
}
echo "✓ 配置上传成功"

# 4. 验证 nginx 配置语法
echo ""
echo "步骤 3/5: 验证 nginx 配置语法..."
ssh ${USER}@${SERVER} "nginx -t" || {
    echo "❌ 配置验证失败，正在回滚..."
    ssh ${USER}@${SERVER} "cp /etc/nginx/conf.d/$BACKUP_NAME $CONFIG_TARGET"
    echo "已回滚到备份配置"
    exit 1
}
echo "✓ 配置语法正确"

# 5. 重载 nginx
echo ""
echo "步骤 4/5: 重载 nginx..."
ssh ${USER}@${SERVER} "systemctl reload nginx" || {
    echo "❌ nginx 重载失败，正在回滚..."
    ssh ${USER}@${SERVER} "cp /etc/nginx/conf.d/$BACKUP_NAME $CONFIG_TARGET && systemctl reload nginx"
    echo "已回滚到备份配置"
    exit 1
}
echo "✓ nginx 重载成功"

# 6. 验证修复
echo ""
echo "步骤 5/5: 验证修复..."
echo "测试 /api/auth/token 端点..."

# 测试登录端点（预期返回 401 或 400，而不是 405）
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST https://llm.kxpms.cn/api/auth/token \
    -H "Content-Type: application/json" \
    -d '{"username":"test","password":"test"}' \
    --max-time 10) || HTTP_CODE="000"

if [ "$HTTP_CODE" = "405" ]; then
    echo "❌ 修复失败: 仍然返回 405"
    echo "   可能需要手动检查配置"
    exit 1
elif [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "400" ] || [ "$HTTP_CODE" = "200" ]; then
    echo "✓ 修复成功: HTTP $HTTP_CODE (正常，不再是 405)"
else
    echo "⚠️  收到非预期状态码: HTTP $HTTP_CODE"
    echo "   请手动验证"
fi

echo ""
echo "=========================================="
echo "✓ 部署完成！"
echo "=========================================="
echo ""
echo "备份文件保存在: /etc/nginx/conf.d/$BACKUP_NAME"
echo ""
echo "如需回滚，请执行:"
echo "  ssh ${USER}@${SERVER} 'cp /etc/nginx/conf.d/$BACKUP_NAME $CONFIG_TARGET && nginx -t && systemctl reload nginx'"
echo ""
echo "建议验证:"
echo "  1. 浏览器访问 https://llm.kxpms.cn 并尝试登录"
echo "  2. 检查 WebSocket 连接: https://llm.kxpms.cn/api/admin/live-stream"
echo "  3. 检查健康检查: curl https://llm.kxpms.cn/healthz"
echo ""
