#!/bin/bash
# 测试环境配置

# 使用本地Docker中的PostgreSQL
export DB_HOST="localhost"
export DB_PORT="5432"
export DB_USER="postgres"
export DB_NAME="llm_gateway_test"
export DB_PASSWORD=""  # Docker容器通常无密码或使用postgres

# 检查数据库连接
echo "测试数据库连接..."
if docker exec llm-gateway-pg psql -U postgres -c "SELECT version();" > /dev/null 2>&1; then
    echo "✓ 数据库连接成功"
    
    # 创建测试数据库（如果不存在）
    docker exec llm-gateway-pg psql -U postgres -c "CREATE DATABASE llm_gateway_test;" 2>/dev/null || echo "  (数据库已存在或创建失败)"
    
    echo ""
    echo "环境配置："
    echo "  DB_HOST: $DB_HOST"
    echo "  DB_PORT: $DB_PORT"
    echo "  DB_NAME: $DB_NAME"
    echo "  Container: llm-gateway-pg"
else
    echo "✗ 无法连接数据库"
    echo "请确保 llm-gateway-pg 容器正在运行"
    exit 1
fi
