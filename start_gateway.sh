#!/bin/bash
# 启动 LLM Gateway 应用

echo "========================================="
echo "🚀 启动 LLM Gateway"
echo "========================================="
echo ""

# 设置环境变量
export LLM_GATEWAY_DATABASE_URL="postgresql://xutaohuang@localhost:5432/llm_gateway"
export LLM_GATEWAY_LISTEN=":8080"
export LLM_GATEWAY_LOG_LEVEL="info"

# 如果有 .env 文件，加载它
if [ -f ".env" ]; then
    echo "📝 加载 .env 文件..."
    set -a
    source .env
    set +a
fi

echo "📊 环境配置:"
echo "  - 数据库: $LLM_GATEWAY_DATABASE_URL"
echo "  - 监听地址: $LLM_GATEWAY_LISTEN"
echo "  - 日志级别: $LLM_GATEWAY_LOG_LEVEL"
echo ""

# 验证数据库连接
echo "🔍 验证数据库连接..."
if psql -d llm_gateway -c "SELECT COUNT(*) FROM v_routable_credential_models WHERE tenant_id = 'default' AND is_routable = TRUE;" > /dev/null 2>&1; then
    ROUTABLE=$(psql -d llm_gateway -t -c "SELECT COUNT(*) FROM v_routable_credential_models WHERE tenant_id = 'default' AND is_routable = TRUE;" 2>/dev/null | tr -d ' ')
    echo "✅ 数据库连接成功"
    echo "✅ 找到 $ROUTABLE 个可路由节点"
else
    echo "❌ 数据库连接失败"
    exit 1
fi
echo ""

echo "🚀 启动应用..."
echo "   访问: http://localhost:8080"
echo "   管理界面: http://localhost:8080/admin"
echo ""
echo "按 Ctrl+C 停止应用"
echo ""
echo "========================================="
echo ""

# 启动应用
go run ./cmd/gateway
