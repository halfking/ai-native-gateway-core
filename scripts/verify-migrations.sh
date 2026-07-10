#!/bin/bash
# 数据库迁移验证脚本
# 验证 360_session_module_executions.sql 和 361_dashboard_access_events.sql

set -e

echo "=========================================="
echo "数据库迁移验证脚本"
echo "=========================================="

# 数据库连接参数（从环境变量获取，或使用默认值）
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-5432}
DB_NAME=${DB_NAME:-llm_gateway}
DB_USER=${DB_USER:-postgres}
DB_PASSWORD=${DB_PASSWORD:-postgres}

# 构造连接字符串
PSQL="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -v ON_ERROR_STOP=1"

echo "数据库: $DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 1. 验证表是否存在
echo "1. 验证表结构..."
$PSQL -c "\d session_module_executions_hot" > /dev/null 2>&1 && echo "✓ session_module_executions_hot 表存在" || echo "✗ session_module_executions_hot 表不存在"
$PSQL -c "\d session_module_executions" > /dev/null 2>&1 && echo "✓ session_module_executions 分区表存在" || echo "✗ session_module_executions 分区表不存在"
$PSQL -c "\d dashboard_access_events_hot" > /dev/null 2>&1 && echo "✓ dashboard_access_events_hot 表存在" || echo "✗ dashboard_access_events_hot 表不存在"
$PSQL -c "\d dashboard_access_events" > /dev/null 2>&1 && echo "✓ dashboard_access_events 分区表存在" || echo "✗ dashboard_access_events 分区表不存在"
echo ""

# 2. 验证索引
echo "2. 验证索引..."
$PSQL -c "SELECT indexname FROM pg_indexes WHERE tablename = 'session_module_executions_hot' ORDER BY indexname;" | grep idx_sme_hot_lookup > /dev/null && echo "✓ idx_sme_hot_lookup 索引存在" || echo "✗ idx_sme_hot_lookup 索引不存在"
$PSQL -c "SELECT indexname FROM pg_indexes WHERE tablename = 'dashboard_access_events_hot' ORDER BY indexname;" | grep idx_dae_hot_timestamp > /dev/null && echo "✓ idx_dae_hot_timestamp 索引存在" || echo "✗ idx_dae_hot_timestamp 索引不存在"
echo ""

# 3. 验证分区
echo "3. 验证分区..."
CURRENT_MONTH=$(date +%Y_%m)
NEXT_MONTH=$(date -v+1m +%Y_%m 2>/dev/null || date -d "+1 month" +%Y_%m)

$PSQL -c "\d session_module_executions_${CURRENT_MONTH}" > /dev/null 2>&1 && echo "✓ session_module_executions_${CURRENT_MONTH} 分区存在" || echo "✗ session_module_executions_${CURRENT_MONTH} 分区不存在"
$PSQL -c "\d session_module_executions_${NEXT_MONTH}" > /dev/null 2>&1 && echo "✓ session_module_executions_${NEXT_MONTH} 分区存在" || echo "✗ session_module_executions_${NEXT_MONTH} 分区不存在"
$PSQL -c "\d dashboard_access_events_${CURRENT_MONTH}" > /dev/null 2>&1 && echo "✓ dashboard_access_events_${CURRENT_MONTH} 分区存在" || echo "✗ dashboard_access_events_${CURRENT_MONTH} 分区不存在"
$PSQL -c "\d dashboard_access_events_${NEXT_MONTH}" > /dev/null 2>&1 && echo "✓ dashboard_access_events_${NEXT_MONTH} 分区存在" || echo "✗ dashboard_access_events_${NEXT_MONTH} 分区不存在"
echo ""

# 4. 验证函数
echo "4. 验证函数..."
$PSQL -c "SELECT proname FROM pg_proc WHERE proname = 'archive_session_module_executions';" | grep archive_session_module_executions > /dev/null && echo "✓ archive_session_module_executions 函数存在" || echo "✗ archive_session_module_executions 函数不存在"
$PSQL -c "SELECT proname FROM pg_proc WHERE proname = 'ensure_session_module_executions_partition';" | grep ensure_session_module_executions_partition > /dev/null && echo "✓ ensure_session_module_executions_partition 函数存在" || echo "✗ ensure_session_module_executions_partition 函数不存在"
$PSQL -c "SELECT proname FROM pg_proc WHERE proname = 'archive_dashboard_events';" | grep archive_dashboard_events > /dev/null && echo "✓ archive_dashboard_events 函数存在" || echo "✗ archive_dashboard_events 函数不存在"
$PSQL -c "SELECT proname FROM pg_proc WHERE proname = 'ensure_dashboard_events_partition';" | grep ensure_dashboard_events_partition > /dev/null && echo "✓ ensure_dashboard_events_partition 函数存在" || echo "✗ ensure_dashboard_events_partition 函数不存在"
echo ""

# 5. 验证视图
echo "5. 验证视图..."
$PSQL -c "\d v_sme_module_stats" > /dev/null 2>&1 && echo "✓ v_sme_module_stats 视图存在" || echo "✗ v_sme_module_stats 视图不存在"
$PSQL -c "\d v_sme_cache_hit_rate" > /dev/null 2>&1 && echo "✓ v_sme_cache_hit_rate 视图存在" || echo "✗ v_sme_cache_hit_rate 视图不存在"
$PSQL -c "\d v_dashboard_access_stats" > /dev/null 2>&1 && echo "✓ v_dashboard_access_stats 视图存在" || echo "✗ v_dashboard_access_stats 视图不存在"
$PSQL -c "\d v_dashboard_slow_queries" > /dev/null 2>&1 && echo "✓ v_dashboard_slow_queries 视图存在" || echo "✗ v_dashboard_slow_queries 视图不存在"
echo ""

# 6. 插入测试数据
echo "6. 插入测试数据..."
$PSQL <<SQL
-- 测试 session_module_executions_hot 插入
INSERT INTO session_module_executions_hot (
    gw_session_id, tenant_id, module_name, cache_key, 
    status, expires_at, result_summary
) VALUES (
    'test_session_001', 'test_tenant', 'session_audit', 'test_key_001',
    'completed', NOW() + INTERVAL '1 hour', '{"score": 5}'::jsonb
) ON CONFLICT DO NOTHING;

-- 测试 dashboard_access_events_hot 插入
INSERT INTO dashboard_access_events_hot (
    event_id, event_type, tenant_id, api_path, api_method, 
    status_code, response_time_ms
) VALUES (
    'test_event_001', 'api_access', 'test_tenant', '/api/admin/dashboard/session-overview', 'GET',
    200, 150
) ON CONFLICT DO NOTHING;
SQL

echo "✓ 测试数据插入成功"
echo ""

# 7. 查询测试
echo "7. 查询测试..."
COUNT_SME=$($PSQL -t -c "SELECT COUNT(*) FROM session_module_executions_hot WHERE gw_session_id = 'test_session_001';")
echo "   session_module_executions_hot 记录数: $COUNT_SME"

COUNT_DAE=$($PSQL -t -c "SELECT COUNT(*) FROM dashboard_access_events_hot WHERE event_id = 'test_event_001';")
echo "   dashboard_access_events_hot 记录数: $COUNT_DAE"
echo ""

# 8. 清理测试数据
echo "8. 清理测试数据..."
$PSQL <<SQL
DELETE FROM session_module_executions_hot WHERE gw_session_id = 'test_session_001';
DELETE FROM dashboard_access_events_hot WHERE event_id = 'test_event_001';
SQL
echo "✓ 测试数据清理完成"
echo ""

# 9. 性能测试（可选）
echo "9. 性能测试..."
echo "   测试索引查询性能..."
$PSQL -c "EXPLAIN ANALYZE SELECT * FROM session_module_executions_hot WHERE gw_session_id = 'test' AND module_name = 'test' AND status = 'completed' LIMIT 1;" > /dev/null 2>&1 && echo "✓ 索引查询正常" || echo "✗ 索引查询异常"
echo ""

echo "=========================================="
echo "验证完成！"
echo "=========================================="
