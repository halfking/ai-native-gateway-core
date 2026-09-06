-- =====================================================================
-- 自检系统数据诊断与修复 SQL 脚本
-- 生成日期: 2026-09-06
-- 用途: 识别并修复导致节点状态不同步的数据问题
-- =====================================================================

-- =====================================================================
-- 诊断 1: 查找所有存在模型绑定歧义的凭据
-- 问题: 多个相似模型名导致 RestoreOnSuccess 失败
-- 影响: 热路径恢复失效，延迟恢复时间
-- =====================================================================

SELECT 
    c.id AS credential_id,
    c.label AS credential_label,
    c.provider_id,
    pv.name AS provider_name,
    pm.standardized_name,
    STRING_AGG(pm.raw_model_name, ', ' ORDER BY pm.raw_model_name) AS ambiguous_models,
    COUNT(DISTINCT pm.raw_model_name) AS model_count,
    STRING_AGG(DISTINCT cmb.available::text, ', ') AS availability_states
FROM credentials c
JOIN providers pv ON pv.id = c.provider_id
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
WHERE c.status = 'active'
  AND c.manual_disabled = FALSE
GROUP BY c.id, c.label, c.provider_id, pv.name, pm.standardized_name
HAVING COUNT(DISTINCT pm.raw_model_name) > 1
ORDER BY model_count DESC, c.id;

-- 预期: 如果有结果，说明存在歧义需要修复
-- 示例输出:
-- credential_id | credential_label | provider_name | standardized_name | ambiguous_models | model_count
-- 42            | minimax-main     | MiniMax       | minimax-m2.7      | MiniMax-M2.7, MiniMax-M2.7-highspeed | 2


-- =====================================================================
-- 诊断 2: 查找 unavailable_recover_at 为 NULL 的不可用绑定
-- 问题: 恢复 SQL 跳过这些行，导致永久不可用
-- 影响: 节点无法自动恢复
-- =====================================================================

SELECT 
    cmb.credential_id,
    c.label AS credential_label,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at,
    EXTRACT(EPOCH FROM (NOW() - cmb.unavailable_at))/60 AS unavailable_minutes
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND cmb.unavailable_recover_at IS NULL
  AND c.status = 'active'
ORDER BY cmb.unavailable_at ASC
LIMIT 100;

-- 预期: P0 修复后应该为空
-- 如果有结果，说明有新的代码路径未设置 unavailable_recover_at


-- =====================================================================
-- 诊断 3: 查找 availability_recover_at 已过期但仍不可用的凭据
-- 问题: 恢复机制失效，凭据未能自动恢复
-- 影响: 手工干预必要性
-- =====================================================================

SELECT 
    c.id AS credential_id,
    c.label,
    c.availability_state,
    c.availability_recover_at,
    EXTRACT(EPOCH FROM (NOW() - c.availability_recover_at))/60 AS overdue_minutes,
    c.state_reason_code,
    c.state_reason_detail,
    COUNT(cmb.id) FILTER (WHERE cmb.available = FALSE) AS unavailable_bindings,
    COUNT(cmb.id) AS total_bindings
FROM credentials c
LEFT JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE c.availability_state IN ('cooling', 'rate_limited', 'unreachable', 'suspended')
  AND c.availability_recover_at IS NOT NULL
  AND c.availability_recover_at < NOW() - INTERVAL '10 minutes'
  AND c.status = 'active'
GROUP BY c.id, c.label, c.availability_state, c.availability_recover_at, 
         c.state_reason_code, c.state_reason_detail
ORDER BY overdue_minutes DESC
LIMIT 50;

-- 预期: 应该很少或为空
-- 如果有大量结果，说明恢复机制有问题


-- =====================================================================
-- 诊断 4: 查找 node_probe_state 缺失的不可用绑定
-- 问题: 无法调度探测，导致永久不可用
-- 影响: 节点无法自动恢复
-- =====================================================================

SELECT 
    c.id AS credential_id,
    c.label,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at,
    nps.credential_id AS has_probe_state,
    EXTRACT(EPOCH FROM (NOW() - cmb.unavailable_at))/60 AS unavailable_minutes
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND nps.credential_id IS NULL
  AND c.status = 'active'
ORDER BY cmb.unavailable_at ASC
LIMIT 100;

-- 预期: 应该很少或为空
-- 如果有结果，需要创建 node_probe_state 记录


-- =====================================================================
-- 诊断 5: 查找 broken_confirmed 模型阻塞的凭据
-- 问题: 一个废弃模型阻止整个凭据的其他健康模型恢复（P0.2 已修复）
-- 影响: 凭据级恢复失效
-- =====================================================================

SELECT 
    c.id AS credential_id,
    c.label,
    c.availability_state,
    COUNT(DISTINCT pm.id) AS total_models,
    COUNT(DISTINCT pm.id) FILTER (WHERE mps.state = 'broken_confirmed') AS broken_models,
    COUNT(DISTINCT pm.id) FILTER (WHERE cmb.available = FALSE) AS unavailable_models,
    STRING_AGG(DISTINCT pm.raw_model_name, ', ') FILTER (WHERE mps.state = 'broken_confirmed') AS broken_model_names
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN model_probe_state mps 
    ON mps.credential_id = c.id 
    AND mps.raw_model_name = pm.raw_model_name
WHERE c.status = 'active'
  AND EXISTS (
      SELECT 1 FROM model_probe_state mps2
      WHERE mps2.credential_id = c.id
        AND mps2.state = 'broken_confirmed'
  )
GROUP BY c.id, c.label, c.availability_state
HAVING COUNT(DISTINCT pm.id) FILTER (WHERE mps.state = 'broken_confirmed') > 0
   AND COUNT(DISTINCT pm.id) FILTER (WHERE cmb.available = FALSE) > 
       COUNT(DISTINCT pm.id) FILTER (WHERE mps.state = 'broken_confirmed')
ORDER BY broken_models DESC, c.id
LIMIT 50;

-- 预期: P0.2 修复后应该减少
-- 如果凭据有健康模型但仍不可用，说明守卫可能仍过严


-- =====================================================================
-- 诊断 6: model_offers 与 credential_model_bindings 不一致
-- 问题: Admin UI 显示不可用，但实际可路由（或反之）
-- 影响: 误导运维人员手工干预
-- =====================================================================

SELECT 
    cmb.credential_id,
    c.label,
    pm.raw_model_name,
    cmb.available AS cmb_available,
    mo.available AS offer_available,
    cmb.unavailable_reason AS cmb_reason,
    mo.unavailable_reason AS offer_reason,
    cmb.updated_at AS cmb_updated,
    mo.updated_at AS offer_updated
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN model_offers mo 
    ON mo.credential_id = cmb.credential_id 
    AND mo.raw_model_name = pm.raw_model_name
WHERE cmb.available != COALESCE(mo.available, TRUE)
   OR cmb.unavailable_reason != mo.unavailable_reason
ORDER BY cmb.updated_at DESC
LIMIT 100;

-- 预期: 应该为空（model_offers 是视图，应自动同步）
-- 如果有结果，说明视图定义或更新逻辑有问题


-- =====================================================================
-- 诊断 7: 长时间未执行的探测任务
-- 问题: next_retry_at 已过期但探测未执行
-- 影响: 节点状态无法更新
-- =====================================================================

SELECT 
    nps.credential_id,
    c.label,
    nps.raw_model_name,
    nps.next_retry_at,
    EXTRACT(EPOCH FROM (NOW() - nps.next_retry_at))/60 AS overdue_minutes,
    nps.consecutive_failures,
    nps.paused,
    nps.last_err_code,
    nps.in_flight_until,
    cmb.available
FROM node_probe_state nps
JOIN credentials c ON c.id = nps.credential_id
LEFT JOIN credential_model_bindings cmb 
    ON cmb.credential_id = nps.credential_id
LEFT JOIN provider_models pm 
    ON pm.raw_model_name = nps.raw_model_name 
    AND pm.id = cmb.provider_model_id
WHERE nps.next_retry_at < NOW() - INTERVAL '5 minutes'
  AND nps.paused = FALSE
  AND (nps.in_flight_until IS NULL OR nps.in_flight_until < NOW())
  AND c.status = 'active'
ORDER BY overdue_minutes DESC
LIMIT 100;

-- 预期: P1.2 修复后应该显著减少
-- 如果有大量结果，说明探测队列积压或 pumpDueStatesToQueue 失效


-- =====================================================================
-- 诊断 8: 统计当前节点状态分布
-- 用途: 了解整体健康状况
-- =====================================================================

SELECT 
    'credentials_by_availability_state' AS metric,
    c.availability_state AS state,
    COUNT(*) AS count
FROM credentials c
WHERE c.status = 'active'
GROUP BY c.availability_state

UNION ALL

SELECT 
    'bindings_by_availability' AS metric,
    CASE WHEN cmb.available THEN 'available' ELSE 'unavailable' END AS state,
    COUNT(*) AS count
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
WHERE c.status = 'active'
GROUP BY cmb.available

UNION ALL

SELECT 
    'bindings_by_unavailable_reason' AS metric,
    COALESCE(cmb.unavailable_reason, 'N/A') AS state,
    COUNT(*) AS count
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
WHERE c.status = 'active'
  AND cmb.available = FALSE
GROUP BY cmb.unavailable_reason

ORDER BY metric, state;

-- 预期: 大部分凭据应该是 'ready'，大部分绑定应该是 available


-- =====================================================================
-- 修复 1: 为缺失 unavailable_recover_at 的行设置恢复时间
-- 注意: 仅在诊断 2 发现问题后执行
-- =====================================================================

-- 预览影响范围（先执行这个）
SELECT 
    COUNT(*) AS affected_rows,
    MIN(unavailable_at) AS earliest_unavailable,
    MAX(unavailable_at) AS latest_unavailable
FROM credential_model_bindings
WHERE available = FALSE
  AND unavailable_reason NOT LIKE 'manual%'
  AND unavailable_recover_at IS NULL
  AND unavailable_at IS NOT NULL;

-- 实际修复（确认影响范围后执行）
-- UPDATE credential_model_bindings
-- SET unavailable_recover_at = unavailable_at + INTERVAL '30 minutes'
-- WHERE available = FALSE
--   AND unavailable_reason NOT LIKE 'manual%'
--   AND unavailable_recover_at IS NULL
--   AND unavailable_at IS NOT NULL;

-- 对于连 unavailable_at 都是 NULL 的行
-- UPDATE credential_model_bindings
-- SET unavailable_recover_at = NOW() + INTERVAL '5 minutes'
-- WHERE available = FALSE
--   AND unavailable_reason NOT LIKE 'manual%'
--   AND unavailable_recover_at IS NULL
--   AND unavailable_at IS NULL;


-- =====================================================================
-- 修复 2: 为缺失 node_probe_state 的不可用绑定创建记录
-- 注意: 仅在诊断 4 发现问题后执行
-- =====================================================================

-- 预览影响范围
SELECT COUNT(*) AS affected_rows
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND nps.credential_id IS NULL;

-- 实际修复（确认影响范围后执行）
-- INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds)
-- SELECT 
--     cmb.credential_id,
--     pm.raw_model_name,
--     NOW(),
--     5
-- FROM credential_model_bindings cmb
-- JOIN credentials c ON c.id = cmb.credential_id
-- JOIN provider_models pm ON pm.id = cmb.provider_model_id
-- LEFT JOIN node_probe_state nps 
--     ON cmb.credential_id = nps.credential_id 
--     AND pm.raw_model_name = nps.raw_model_name
-- WHERE cmb.available = FALSE
--   AND cmb.unavailable_reason NOT LIKE 'manual%'
--   AND nps.credential_id IS NULL
--   AND c.status = 'active'
-- ON CONFLICT (credential_id, raw_model_name) DO NOTHING;


-- =====================================================================
-- 修复 3: 重置长时间过期的探测任务
-- 注意: 仅在诊断 7 发现大量积压后执行
-- =====================================================================

-- 预览影响范围
SELECT 
    COUNT(*) AS affected_rows,
    MIN(next_retry_at) AS earliest_retry,
    MAX(next_retry_at) AS latest_retry
FROM node_probe_state
WHERE next_retry_at < NOW() - INTERVAL '10 minutes'
  AND paused = FALSE
  AND (in_flight_until IS NULL OR in_flight_until < NOW());

-- 实际修复（确认影响范围后执行）
-- UPDATE node_probe_state
-- SET 
--     next_retry_at = NOW(),
--     next_retry_seconds = 5,
--     updated_at = NOW()
-- WHERE next_retry_at < NOW() - INTERVAL '10 minutes'
--   AND paused = FALSE
--   AND (in_flight_until IS NULL OR in_flight_until < NOW());


-- =====================================================================
-- 查询辅助工具: 查看特定凭据的完整状态
-- 用法: 将 $CREDENTIAL_ID 替换为实际的凭据 ID
-- =====================================================================

-- 示例: 查看 credential_id=42 的完整状态
DO $$
DECLARE
    target_credential_id INT := 42;  -- 修改这里
BEGIN
    RAISE NOTICE '=== Credential Level ===';
    PERFORM format('credential_id: %s, label: %s, availability_state: %s, quota_state: %s',
        id, label, availability_state, quota_state)
    FROM credentials WHERE id = target_credential_id;
    
    RAISE NOTICE '=== Model Bindings ===';
    PERFORM format('model: %s, available: %s, reason: %s, recover_at: %s',
        pm.raw_model_name, cmb.available, cmb.unavailable_reason, cmb.unavailable_recover_at)
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.credential_id = target_credential_id;
    
    RAISE NOTICE '=== Probe States ===';
    PERFORM format('model: %s, next_retry: %s, failures: %s, paused: %s',
        raw_model_name, next_retry_at, consecutive_failures, paused)
    FROM node_probe_state
    WHERE credential_id = target_credential_id;
END $$;

-- 或使用简单查询
\set CREDENTIAL_ID 42

SELECT 'Credential' AS level, 
       c.id, c.label, c.availability_state, c.quota_state,
       c.state_reason_code, c.state_reason_detail
FROM credentials c WHERE c.id = :CREDENTIAL_ID

UNION ALL

SELECT 'Binding' AS level,
       cmb.credential_id, pm.raw_model_name, 
       cmb.available::text, cmb.unavailable_reason,
       NULL, cmb.unavailable_recover_at::text
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.credential_id = :CREDENTIAL_ID

UNION ALL

SELECT 'Probe' AS level,
       nps.credential_id, nps.raw_model_name,
       nps.next_retry_at::text, nps.consecutive_failures::text,
       nps.last_err_code, nps.paused::text
FROM node_probe_state nps
WHERE nps.credential_id = :CREDENTIAL_ID;


-- =====================================================================
-- 使用说明
-- =====================================================================

-- 1. 先运行所有诊断查询 (诊断 1-8)，了解当前状态
-- 2. 根据诊断结果，决定是否需要执行修复
-- 3. 执行修复前，先运行预览查询，确认影响范围
-- 4. 执行修复后，重新运行诊断查询，验证修复效果
-- 5. 定期（每周）运行诊断查询，监控系统健康状况

-- 注意事项:
-- - 所有修复 SQL 默认注释，需要手动取消注释后执行
-- - 修复操作会影响生产数据，建议在低峰期执行
-- - 执行前做好数据备份
-- - 建议先在测试环境验证

-- =====================================================================
-- 文档版本: v1.0
-- 生成时间: 2026-09-06
-- 维护者: 参考 SELFCHECK_CODE_IMPROVEMENTS_20260906.md
-- =====================================================================
