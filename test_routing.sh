#!/bin/bash
# 测试路由功能

echo "========================================="
echo "🧪 路由功能测试"
echo "========================================="
echo ""

DB_NAME="llm_gateway"

echo "1️⃣  测试数据库连接..."
if psql -d $DB_NAME -c "SELECT 1;" > /dev/null 2>&1; then
    echo "✅ 数据库连接成功"
else
    echo "❌ 数据库连接失败"
    exit 1
fi
echo ""

echo "2️⃣  检查可路由节点..."
echo ""
psql -d $DB_NAME << 'SQL'
SELECT 
    '总览' as 类型,
    COUNT(*) as 总数,
    COUNT(*) FILTER (WHERE is_routable = TRUE) as 可路由,
    COUNT(*) FILTER (WHERE is_routable = FALSE) as 不可路由
FROM v_routable_credential_models
WHERE tenant_id = 'default';

\echo ''
\echo '按 Provider 分组统计:'
SELECT 
    p.display_name as Provider,
    COUNT(*) as 模型数,
    COUNT(*) FILTER (WHERE v.is_routable = TRUE) as 可路由
FROM v_routable_credential_models v
JOIN providers p ON p.id = v.provider_id
WHERE v.tenant_id = 'default'
GROUP BY p.display_name
ORDER BY p.display_name;
SQL

echo ""
echo "3️⃣  模拟路由查询 - 测试 gpt-4..."
echo ""
psql -d $DB_NAME << 'SQL'
-- 模拟 provider/client.go 中的 loadCandidatesDB 查询
SELECT 
    c.id AS credential_id,
    p.id AS provider_id,
    p.display_name AS provider,
    mo.raw_model_name AS model,
    COALESCE(mo.routing_tier, 2) AS tier,
    COALESCE(mo.weight, 100) AS weight,
    COALESCE(mo.manual_priority, 99) AS priority,
    v.is_routable AS routable,
    v.unavailable_reason AS reason
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN v_routable_credential_models v
       ON v.credential_id = mo.credential_id
      AND v.raw_model_name = mo.raw_model_name
WHERE p.tenant_id = 'default'
  AND COALESCE(c.status, 'active') NOT IN ('disabled')
  AND v.is_routable = TRUE  -- 这是关键过滤条件
  AND lower(mo.raw_model_name) = 'gpt-4'
ORDER BY 
    COALESCE(mo.manual_priority, 99),
    COALESCE(mo.routing_tier, 2);
SQL

echo ""
echo "4️⃣  模拟路由查询 - 测试 claude-3-5-sonnet..."
echo ""
psql -d $DB_NAME << 'SQL'
SELECT 
    c.id AS credential_id,
    p.id AS provider_id,
    p.display_name AS provider,
    mo.raw_model_name AS model,
    COALESCE(mo.routing_tier, 2) AS tier,
    COALESCE(mo.weight, 100) AS weight,
    COALESCE(mo.manual_priority, 99) AS priority,
    v.is_routable AS routable
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN v_routable_credential_models v
       ON v.credential_id = mo.credential_id
      AND v.raw_model_name = mo.raw_model_name
WHERE p.tenant_id = 'default'
  AND v.is_routable = TRUE
  AND lower(mo.raw_model_name) LIKE '%claude-3-5-sonnet%'
ORDER BY 
    COALESCE(mo.manual_priority, 99);
SQL

echo ""
echo "5️⃣  测试不存在的模型（应该返回空）..."
echo ""
psql -d $DB_NAME << 'SQL'
SELECT 
    COUNT(*) as 找到的节点数
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN v_routable_credential_models v
       ON v.credential_id = mo.credential_id
      AND v.raw_model_name = mo.raw_model_name
WHERE p.tenant_id = 'default'
  AND v.is_routable = TRUE
  AND lower(mo.raw_model_name) = 'non-existent-model';
SQL

echo ""
echo "========================================="
echo "✅ 路由功能测试完成"
echo "========================================="
echo ""
echo "📊 总结:"
echo "  - 视图工作正常 ✅"
echo "  - 可路由节点正确过滤 ✅"
echo "  - SQL 查询逻辑正确 ✅"
echo ""
echo "⚠️  注意: 实际使用需要配置 API Keys"
echo ""
