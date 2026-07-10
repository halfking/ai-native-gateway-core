#!/bin/bash
# 极简本地部署脚本 - 跳过所有schema检查，直接启动服务
# 用途：用于Dashboard前端开发测试，不依赖完整数据库

set -e

PROJ_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJ_ROOT"

echo "========================================"
echo "LLM Gateway 极简部署（无数据库模式）"
echo "========================================"
echo ""

# 停止旧服务
if [ -f /tmp/llm-gateway.pid ]; then
    kill $(cat /tmp/llm-gateway.pid) 2>/dev/null || true
    rm -f /tmp/llm-gateway.pid
    sleep 2
    echo "✓ 已停止旧服务"
fi

# 生成配置（不配置数据库）
cat > /tmp/llm-gateway-minimal.env << 'EOF'
# 极简配置 - 无数据库模式
export LLM_GATEWAY_DATABASE_URL=""
export LLM_GATEWAY_REDIS_ADDR=""
export LLM_GATEWAY_LISTEN=":8781"
export LLM_GATEWAY_SECRET_KEY="minimal-test-secret-key-12345678901234567890"
export LLM_GATEWAY_ADMIN_PASSWORD="<ADMIN_PASSWORD_REDACTED>"
export LLM_GATEWAY_CORS_ORIGINS="*"
export LLM_GATEWAY_ENV="development"
export LLM_GATEWAY_LOG_LEVEL="info"
EOF

echo "✓ 配置已生成（无数据库模式）"

# 编译
echo ""
echo "编译服务..."
if go build -o llm-gateway ./cmd/gateway; then
    echo "✓ 编译成功"
else
    echo "✗ 编译失败"
    exit 1
fi

# 启动
echo ""
echo "启动服务..."
source /tmp/llm-gateway-minimal.env
nohup ./llm-gateway > /tmp/llm-gateway-minimal.log 2>&1 &
echo $! > /tmp/llm-gateway.pid
sleep 5

# 验证
if curl -s http://localhost:8781/healthz | grep -q "ok"; then
    echo "✓ 服务启动成功"
    echo ""
    echo "========================================"
    echo "部署完成"
    echo "========================================"
    echo ""
    echo "访问: http://localhost:8781/"
    echo "日志: tail -f /tmp/llm-gateway-minimal.log"
    echo ""
    echo "注意: 此模式下数据库功能不可用"
    echo "      仅用于前端开发和基础功能测试"
    echo ""
else
    echo "✗ 服务启动失败"
    echo "查看日志: tail -f /tmp/llm-gateway-minimal.log"
    exit 1
fi
