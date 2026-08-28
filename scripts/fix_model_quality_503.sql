-- 模型智商 503 问题 - 数据库修复脚本
-- 执行此脚本后重启网关服务

-- 1. 检查当前设置
SELECT 
    key, 
    value, 
    value_type,
    updated_at,
    updated_by
FROM settings_kv 
WHERE key LIKE 'model_quality.%';

-- 2. 删除可能导致问题的设置（使用代码默认值）
DELETE FROM settings_kv WHERE key = 'model_quality.enabled';

-- 或者显式设置为 true
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by)
VALUES ('model_quality.enabled', 'true', 'boolean', 'platform', 'model_quality', 'fix_503')
ON CONFLICT (key) DO UPDATE SET 
    value = 'true',
    updated_at = NOW(),
    prev_value = settings_kv.value,
    prev_updated_at = settings_kv.updated_at,
    updated_by = 'fix_503';

-- 3. 验证修改
SELECT 
    key, 
    value, 
    value_type,
    updated_at
FROM settings_kv 
WHERE key = 'model_quality.enabled';

-- 4. 执行后需要重启网关服务
-- systemctl restart llm-gateway

-- 5. 重启后检查日志
-- tail -f /var/log/llm-gateway/gateway.log | grep "model_quality_worker started"
