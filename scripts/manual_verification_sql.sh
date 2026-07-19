#!/bin/bash
# ============================================================================
# 简化版验证脚本 - 生成可复制粘贴的SQL命令
# Date: 2026-07-19
# Usage: 复制输出的SQL到你的数据库客户端执行，然后把结果发给我
# ============================================================================

cat << 'EOF'
================================================================
  LLM Gateway 问题诊断 - 手动执行SQL版本
================================================================

请按顺序复制以下SQL到你的数据库客户端（如 psql, DBeaver, pgAdmin）
执行后，把结果发给我进行分析。

----------------------------------------------------------------
查询1: 智谱AI最近24小时的错误类型（最关键）
----------------------------------------------------------------

SELECT
    error_kind,
    upstream_status_code,
    COUNT(*) as error_count,
    MAX(created_at) as last_seen,
    STRING_AGG(DISTINCT SUBSTRING(upstream_response_preview, 1, 100), ' | ') as sample_responses
FROM request_logs
WHERE provider_code = 'zhipuai'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
GROUP BY error_kind, upstream_status_code
ORDER BY error_count DESC
LIMIT 10;

----------------------------------------------------------------
查询2: 降级模式触发次数（最关键）
----------------------------------------------------------------

WITH unavailable_creds AS (
    SELECT
        credential_id,
        raw_model_name,
        unavailable_at,
        unavailable_reason
    FROM credential_model_bindings cmb
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.available = FALSE
      AND cmb.unavailable_at > now() - interval '1 hour'
)
SELECT
    uc.credential_id,
    uc.raw_model_name,
    uc.unavailable_reason,
    COUNT(rl.id) as requests_after_unavailable,
    MAX(rl.created_at) as last_request_at
FROM unavailable_creds uc
LEFT JOIN request_logs rl
    ON rl.credential_id = uc.credential_id
    AND rl.created_at > uc.unavailable_at
GROUP BY uc.credential_id, uc.raw_model_name, uc.unavailable_reason
ORDER BY requests_after_unavailable DESC
LIMIT 10;

----------------------------------------------------------------
查询3: 智谱AI模型配置
----------------------------------------------------------------

SELECT
    pm.raw_model_name,
    pm.canonical_name,
    COUNT(cmb.id) as total_bindings,
    COUNT(CASE WHEN cmb.available = TRUE THEN 1 END) as available_bindings,
    COUNT(CASE WHEN cmb.available = FALSE THEN 1 END) as unavailable_bindings
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
WHERE p.provider_code = 'zhipuai'
GROUP BY pm.raw_model_name, pm.canonical_name
ORDER BY pm.raw_model_name;

----------------------------------------------------------------
查询4: 火山引擎 GLM 模型配置（验证是否已添加）
----------------------------------------------------------------

SELECT
    p.provider_code,
    pm.raw_model_name,
    pm.canonical_name,
    COUNT(cmb.id) as binding_count
FROM provider_models pm
JOIN providers p ON p.id = pm.provider_id
LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
WHERE p.provider_code IN ('volcano', 'volc', 'volcengine', 'bytedance')
  AND (
    LOWER(pm.raw_model_name) LIKE '%glm%'
    OR LOWER(pm.canonical_name) LIKE '%glm%'
  )
GROUP BY p.provider_code, pm.raw_model_name, pm.canonical_name
ORDER BY pm.raw_model_name;

----------------------------------------------------------------
查询5: 商汤最近的错误（如果有）
----------------------------------------------------------------

SELECT
    error_kind,
    upstream_status_code,
    COUNT(*) as error_count,
    MAX(created_at) as last_seen
FROM request_logs
WHERE provider_code LIKE '%sensenova%'
  AND request_status = 'failure'
  AND created_at > now() - interval '24 hours'
GROUP BY error_kind, upstream_status_code
ORDER BY error_count DESC
LIMIT 10;

================================================================
执行说明：
================================================================

1. 连接到你的生产数据库
2. 逐个执行上面的5个查询
3. 把查询结果（可以截图或复制文本）发给我

最关键的是 查询1 和 查询2，它们能直接确认问题！

================================================================
快速判断：
================================================================

查询1 结果：
- 如果 error_kind = 'rate_limit' 且 upstream_status_code = 429
  → 错误分类正确 ✓

查询2 结果：
- 如果有数据且 requests_after_unavailable > 0
  → 降级模式确实在使用不可用的凭证！（问题确认）

================================================================
EOF
