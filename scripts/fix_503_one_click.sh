#!/bin/bash
# 模型智商 503 紧急修复 - 一键执行脚本
# 在服务器上执行此脚本

set -e

echo "=== 模型智商 503 紧急修复 ==="
echo ""

echo "步骤 1: 检查数据库设置"
CURRENT_VALUE=$(psql -U postgres -d llm_gateway -t -c "SELECT value FROM settings_kv WHERE key = 'model_quality.enabled';" 2>/dev/null | tr -d ' ')

if [ -z "$CURRENT_VALUE" ]; then
    echo "✓ 数据库中没有设置，使用代码默认值（true）"
elif [ "$CURRENT_VALUE" = "false" ]; then
    echo "✗ 发现问题：数据库中设置为 false"
    echo ""
    echo "步骤 2: 删除数据库设置"
    psql -U postgres -d llm_gateway -c "DELETE FROM settings_kv WHERE key = 'model_quality.enabled';"
    echo "✓ 已删除，将使用代码默认值（true）"
else
    echo "✓ 数据库设置为: $CURRENT_VALUE"
fi

echo ""
echo "步骤 3: 重启网关服务"
systemctl restart llm-gateway

echo "✓ 服务已重启"
echo ""
echo "步骤 4: 等待服务启动（5秒）"
sleep 5

echo ""
echo "步骤 5: 检查日志"
if grep -q "model_quality_worker started" /var/log/llm-gateway/gateway.log; then
    echo "✓ 模型质量服务已启动！"
    echo ""
    grep "model_quality_worker started" /var/log/llm-gateway/gateway.log | tail -1
else
    echo "✗ 未找到启动日志，请手动检查："
    echo "   tail -50 /var/log/llm-gateway/gateway.log"
fi

echo ""
echo "=== 修复完成 ==="
echo ""
echo "测试命令："
echo "curl -X POST 'https://llm.kxpms.cn/api/admin/model-iq/trigger' \\"
echo "  -H 'Authorization: Bearer YOUR_TOKEN' \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{\"credential_id\": 42, \"raw_model_name\": \"glm-5.2\"}'"
