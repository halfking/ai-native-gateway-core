#!/bin/bash
# 路由问题诊断和自动修复脚本

set -e

# 数据库连接信息（从环境变量或默认值获取）
DB_NAME="${DB_NAME:-llm_gateway}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"

echo "========================================="
echo "LLM Gateway 路由问题诊断和修复"
echo "========================================="
echo ""

# 检查 psql 是否可用
if ! command -v psql &> /dev/null; then
    echo "❌ 错误: psql 命令不可用，请安装 PostgreSQL 客户端"
    exit 1
fi

# 测试数据库连接
echo "📊 步骤 1/5: 测试数据库连接..."
if ! PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" > /dev/null 2>&1; then
    echo "❌ 无法连接到数据库。请检查连接信息或启动数据库。"
    echo "   数据库: $DB_NAME@$DB_HOST:$DB_PORT"
    echo ""
    echo "💡 如果数据库未启动，可以尝试："
    echo "   brew services start postgresql"
    echo "   或"
    echo "   pg_ctl -D /usr/local/var/postgres start"
    exit 1
fi
echo "✅ 数据库连接成功"
echo ""

# 检查视图是否存在
echo "🔍 步骤 2/5: 检查 v_routable_credential_models 视图..."
VIEW_EXISTS=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'v_routable_credential_models';" | tr -d ' ')

if [ "$VIEW_EXISTS" = "0" ]; then
    echo "❌ 视图不存在！这就是问题所在。"
    echo ""
    echo "📝 正在创建视图..."
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f fix_routing_issue.sql
    echo "✅ 视图创建完成"
else
    echo "✅ 视图存在"
    
    # 检查可路由节点数量
    echo ""
    echo "📊 步骤 3/5: 检查可路由节点数量..."
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF'
\x on
SELECT 
    COUNT(*) as total_records,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable_count,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as not_routable_count,
    COUNT(*) FILTER (WHERE is_routable IS NULL) as null_routable,
    COUNT(DISTINCT credential_id) as unique_credentials,
    COUNT(DISTINCT raw_model_name) as unique_models
FROM v_routable_credential_models
WHERE tenant_id = 'default';
\x off
EOF

    ROUTABLE_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT COUNT(*) FROM v_routable_credential_models WHERE tenant_id = 'default' AND is_routable = TRUE;" | tr -d ' ')
    
    if [ "$ROUTABLE_COUNT" = "0" ]; then
        echo ""
        echo "⚠️  警告: 没有可路由的节点！"
        echo ""
        echo "📊 步骤 4/5: 分析不可路由原因..."
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF'
SELECT 
    COALESCE(unavailable_reason, 'NULL') as reason,
    COUNT(*) as count
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = FALSE
GROUP BY unavailable_reason
ORDER BY count DESC
LIMIT 10;
EOF
        
        echo ""
        echo "🔧 是否要自动修复？这将："
        echo "   1. 恢复所有 providers 为启用状态"
        echo "   2. 恢复所有 credentials 为 ready 状态"
        echo "   3. 恢复所有 model_offers 为可用状态"
        echo ""
        read -p "继续修复? (y/N): " -n 1 -r
        echo
        
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            echo ""
            echo "🔧 步骤 5/5: 执行自动修复..."
            PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF'
-- 1. 启用所有 providers
UPDATE providers
SET 
    enabled = TRUE,
    manual_disabled = FALSE
WHERE tenant_id = 'default';

-- 2. 恢复所有 credentials
UPDATE credentials
SET 
    availability_state = 'ready',
    availability_recover_at = NULL,
    quota_state = 'ok',
    quota_recover_at = NULL,
    lifecycle_status = 'active',
    status = 'active',
    circuit_state = 'closed',
    consecutive_failures = 0,
    cooling_until = NULL,
    state_reason_code = NULL,
    state_reason_detail = NULL,
    state_updated_at = now()
WHERE tenant_id = 'default'
AND lifecycle_status != 'archived';

-- 3. 恢复所有 model_offers
UPDATE model_offers mo
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL
FROM credentials c
WHERE mo.credential_id = c.id
AND c.tenant_id = 'default'
AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
AND COALESCE(mo.admin_protected, FALSE) = FALSE;

-- 4. 恢复 credential_model_bindings（如果存在）
UPDATE credential_model_bindings cmb
SET 
    available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL
FROM credentials c
WHERE cmb.credential_id = c.id
AND c.tenant_id = 'default'
AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
AND COALESCE(cmb.admin_protected, FALSE) = FALSE;

-- 显示修复结果
SELECT 'Providers updated:' as action, COUNT(*) as count FROM providers WHERE tenant_id = 'default' AND enabled = TRUE
UNION ALL
SELECT 'Credentials updated:', COUNT(*) FROM credentials WHERE tenant_id = 'default' AND availability_state = 'ready'
UNION ALL
SELECT 'Model offers updated:', COUNT(*) FROM model_offers mo JOIN credentials c ON mo.credential_id = c.id WHERE c.tenant_id = 'default' AND mo.available = TRUE;
EOF
            
            echo ""
            echo "✅ 修复完成！正在验证..."
            echo ""
            PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF'
SELECT 
    COUNT(*) FILTER (WHERE is_routable = TRUE) as routable_nodes,
    COUNT(DISTINCT credential_id) as routable_credentials,
    COUNT(DISTINCT raw_model_name) as routable_models
FROM v_routable_credential_models
WHERE tenant_id = 'default';
EOF
            
            echo ""
            echo "🎉 修复完成！请重启应用以使缓存失效。"
        else
            echo "❌ 用户取消修复"
            exit 1
        fi
    else
        echo "✅ 找到 $ROUTABLE_COUNT 个可路由节点，路由配置正常"
        echo ""
        echo "📊 显示一些可路由节点样本："
        PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << 'EOF'
SELECT 
    provider_id,
    credential_id,
    raw_model_name,
    is_routable
FROM v_routable_credential_models
WHERE tenant_id = 'default' AND is_routable = TRUE
LIMIT 10;
EOF
    fi
fi

echo ""
echo "========================================="
echo "诊断完成"
echo "========================================="
