-- ============================================================================
-- 生产环境修复脚本：会话压缩配置修复
-- 
-- 问题: gpt-5.6-luna 频繁出现 context_length_exceeded
-- 根因: 第三方中转节点(apigpt/apiclaude)禁用了会话压缩，导致完整历史直接转发
-- 修复: 启用智能压缩模式
--
-- 执行环境: 生产数据库
-- 预计影响: 中转节点的所有请求（压缩功能恢复）
-- 回滚方案: 见文件末尾
-- ============================================================================

-- 1. 备份当前配置
CREATE TEMP TABLE provider_settings_backup_20260719 AS
SELECT * FROM provider_settings 
WHERE provider_id IN (587, 2451) 
  AND key = 'compression.mode';

-- 2. 显示修复前状态
SELECT 
    '=== 修复前配置 ===' AS info,
    p.id AS provider_id,
    p.label AS provider_name,
    p.category,
    ps.key,
    ps.value AS current_value,
    ps.enabled
FROM providers p
LEFT JOIN provider_settings ps 
    ON ps.provider_id = p.id 
    AND ps.key = 'compression.mode'
WHERE p.id IN (587, 2451);

-- 3. 执行修复
BEGIN;

-- 方案: 将 compression.mode 从 "off" 改为 "smart"
UPDATE provider_settings 
SET 
    value = '"smart"',
    updated_at = NOW(),
    updated_by = 'system'
WHERE provider_id IN (587, 2451) 
  AND key = 'compression.mode'
  AND value = '"off"';

-- 如果记录不存在，插入默认值
INSERT INTO provider_settings (provider_id, key, value, enabled, source, created_by, updated_by)
SELECT 
    p.id,
    'compression.mode',
    '"smart"',
    true,
    'admin',
    'system',
    'system'
FROM providers p
WHERE p.id IN (587, 2451)
  AND NOT EXISTS (
      SELECT 1 FROM provider_settings ps 
      WHERE ps.provider_id = p.id 
        AND ps.key = 'compression.mode'
  )
ON CONFLICT (provider_id, key) DO UPDATE 
SET 
    value = '"smart"',
    enabled = true,
    updated_at = NOW(),
    updated_by = 'system';

-- 4. 修复 gpt-5.6-luna 的 NULL contextWindow
UPDATE models_canonical 
SET 
    context_window = 128000,
    updated_at = NOW()
WHERE canonical_name = 'gpt-5.6-luna' 
  AND context_window IS NULL;

-- 5. 验证修复结果
SELECT 
    '=== 修复后配置 ===' AS info,
    p.id AS provider_id,
    p.label AS provider_name,
    p.category,
    ps.key,
    ps.value AS new_value,
    ps.enabled,
    ps.updated_at
FROM providers p
LEFT JOIN provider_settings ps 
    ON ps.provider_id = p.id 
    AND ps.key = 'compression.mode'
WHERE p.id IN (587, 2451);

-- 6. 验证 context_window
SELECT 
    '=== Context Window 状态 ===' AS info,
    canonical_name,
    context_window,
    family,
    status
FROM models_canonical
WHERE canonical_name LIKE '%luna%';

-- 7. 显示受影响的凭据数量
SELECT 
    '=== 受影响的凭据 ===' AS info,
    p.label AS provider_name,
    COUNT(DISTINCT c.id) AS credential_count,
    COUNT(DISTINCT mo.raw_model_name) AS model_count
FROM providers p
JOIN credentials c ON c.provider_id = p.id
JOIN model_offers mo ON mo.credential_id = c.id
WHERE p.id IN (587, 2451)
  AND c.status = 'active'
  AND mo.available = true
GROUP BY p.id, p.label;

-- 确认提交（需要手动执行）
-- COMMIT;

-- ============================================================================
-- 回滚方案（如需回滚，执行以下语句）
-- ============================================================================

-- 回滚步骤 1: 恢复原始配置
/*
BEGIN;

UPDATE provider_settings ps
SET 
    value = backup.value,
    enabled = backup.enabled,
    updated_at = NOW(),
    updated_by = 'system-rollback'
FROM provider_settings_backup_20260719 backup
WHERE ps.provider_id = backup.provider_id
  AND ps.key = backup.key;

COMMIT;
*/

-- 回滚步骤 2: 验证回滚结果
/*
SELECT 
    p.id AS provider_id,
    p.label AS provider_name,
    ps.value AS compression_mode
FROM providers p
LEFT JOIN provider_settings ps 
    ON ps.provider_id = p.id 
    AND ps.key = 'compression.mode'
WHERE p.id IN (587, 2451);
*/

-- ============================================================================
-- 监控查询（部署后执行，观察 15-30 分钟）
-- ============================================================================

-- 监控查询 1: 压缩效果实时监控
/*
SELECT 
    DATE_TRUNC('minute', created_at) AS minute,
    p.label AS provider_name,
    COUNT(*) AS total_requests,
    COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) AS compressed_requests,
    ROUND(100.0 * COUNT(CASE WHEN compression_strategy IS NOT NULL THEN 1 END) / COUNT(*), 2) AS compression_rate_pct,
    COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) AS context_errors,
    AVG(pg_column_size(request_body)) AS avg_request_bytes,
    AVG(pg_column_size(outbound_body)) AS avg_outbound_bytes,
    ROUND(AVG(outbound_msg_count::float / NULLIF(jsonb_array_length(request_body->'messages'), 0)), 2) AS avg_compression_ratio
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.created_at > NOW() - INTERVAL '30 minutes'
  AND p.id IN (587, 2451)
  AND rl.gw_session_id IS NOT NULL
GROUP BY minute, p.label
ORDER BY minute DESC, p.label;
*/

-- 监控查询 2: 错误率监控
/*
SELECT 
    p.label AS provider_name,
    COUNT(*) AS total_requests,
    COUNT(CASE WHEN error_kind IS NOT NULL THEN 1 END) AS error_count,
    COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) AS context_errors,
    ROUND(100.0 * COUNT(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 END) / COUNT(*), 2) AS context_error_rate_pct
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.created_at > NOW() - INTERVAL '30 minutes'
  AND p.id IN (587, 2451)
GROUP BY p.label;
*/

-- 监控查询 3: 大会话处理情况
/*
SELECT 
    rl.gw_session_id,
    p.label AS provider_name,
    COUNT(*) AS request_count,
    MAX(jsonb_array_length(rl.request_body->'messages')) AS max_client_msgs,
    MAX(rl.outbound_msg_count) AS max_outbound_msgs,
    MAX(rl.compression_strategy) AS last_compression_strategy,
    MAX(CASE WHEN error_kind = 'context_length_exceeded' THEN 1 ELSE 0 END) AS has_error
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.created_at > NOW() - INTERVAL '30 minutes'
  AND p.id IN (587, 2451)
  AND rl.gw_session_id IS NOT NULL
GROUP BY rl.gw_session_id, p.label
HAVING MAX(jsonb_array_length(rl.request_body->'messages')) > 50
ORDER BY max_client_msgs DESC
LIMIT 20;
*/

-- ============================================================================
-- 预期结果
-- ============================================================================
-- 
-- 修复后 15-30 分钟内应该观察到：
-- 
-- 1. compression_rate_pct > 70% （70%+ 的请求被压缩）
-- 2. context_error_rate_pct < 1% （上下文错误率降至 1% 以下）
-- 3. avg_compression_ratio < 0.9 （平均压缩比小于 0.9）
-- 4. 大会话（>50条消息）的 max_outbound_msgs 稳定在 40-50
-- 
-- 如果观察到以上指标，说明修复成功！
-- ============================================================================
