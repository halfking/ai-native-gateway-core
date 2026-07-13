-- =============================================================================
-- fix-glm52-alias-drift.sql
-- v2 (2026-07-13): 真实根因改写
--
-- 上一版注释里写的"根因"是 model_aliases 把 glm-5.2 关联到 glm-5.1 的
-- canonical_id (=49)。那是 2026-07-09 第一次出现该 bug 时的猜测。
--
-- 2026-07-13 在 252 的 pg-252-pg17/llm_gateway 现场 audit 之后确认：
--   * model_aliases 表里根本没有 glm-5.2 的 alias 行（不影响本问题）
--   * models_canonical 表里 glm-5.2 (id=173264) 存在且正确
--   * provider_models 中 raw_model_name 形如 glm-5.2 的 6 条记录，
--     outbound_model_name 列全部被错填为 'glm-5.1'
--
-- 真实数据（pm.id, provider_id, raw_model_name, outbound_model_name, created_at）：
--   209423 / 18  / nvidia         / z-ai/glm-5.2 / glm-5.1 / 2026-07-03
--   182701 / 24  / sensenova      / glm-5.2      / glm-5.1 / 2026-07-02
--   170383 / 32  / zhipu          / glm-5.2      / glm-5.1 / 2026-06-15
--   828035 / 37  / scnet          / GLM-5.2      / glm-5.1 / 2026-06-24
--   535060 / 581 / glm-xianyu     / glm-5.2      / glm-5.1 / 2026-06-20
--   600856 / 847 / glm-5.2-oneday / glm-5.2      / glm-5.1 / 2026-06-21
--
-- 请求路径：
--   client request: model = "glm-5.2"
--   provider/client.go:loadCandidatesByModalityDB 命中 raw_model_name='glm-5.2'
--     → provider/client.go:749 cand.RawModel = COALESCE(outbound_model_name, raw_model_name)
--     → 由于 outbound_model_name='glm-5.1'，cand.RawModel='glm-5.1'
--   domains/streaming/executors/executor_chat.go:resolveOutboundModel/prepareRequestBody
--     →  outboundModel != clientModel 时 replaceModelInRequestBody 把 body 里
--        "model":"glm-5.2" 替换为 "model":"glm-5.1"
--   上游供应商实际收到 glm-5.1
--
-- 修复策略：把 glm-5.2 的 outbound_model_name 清空为 NULL，让 COALESCE 兜底
--           走 raw_model_name='glm-5.2'，请求原样转发给供应商。
-- 依据：provider_catalog seed (zhipu 行) 显示 zhipu 上游模型清单已经包含
--       'glm-5.2'，所以直接用 'glm-5.2' 字面调用是被接受的。
--       对照组 glm-5.1 / glm-5-2-260617 的 outbound_model_name 在多数 provider
--       上本来就是 NULL（一致行为）。
--
-- 用法（仍然分 3 部分，DRY-RUN → COMMIT）：
--   ssh -p 25022 root@115.29.212.252 \
--     "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway \
--      -v ON_ERROR_STOP=1 -f /root/glm52_fix.sql"
-- =============================================================================

\echo '========================================================'
\echo ' 第 1 部分：检测（只读）'
\echo '========================================================'

-- 1.1 列出将被清空 outbound_model_name 的 provider_models 行
\echo '--- 1.1 provider_models 中 raw_model_name 含 glm-5.2 且 outbound_model_name = glm-5.1 ---'
SELECT
    pm.id,
    pm.provider_id,
    p.code        AS provider_code,
    pm.raw_model_name,
    pm.outbound_model_name,
    pm.canonical_id,
    mc.canonical_name,
    pm.created_at
FROM provider_models pm
LEFT JOIN providers p         ON p.id  = pm.provider_id
LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
WHERE pm.raw_model_name ILIKE '%glm-5.2%'
  AND pm.outbound_model_name = 'glm-5.1'
ORDER BY pm.id;

-- 1.2 关联的 credential_model_bindings（间接影响 model_offers 视图）
\echo '--- 1.2 通过 provider_model_id 关联的 credential_model_bindings ---'
SELECT
    b.id              AS binding_id,
    b.credential_id,
    pm.raw_model_name,
    pm.outbound_model_name
FROM credential_model_bindings b
JOIN provider_models pm ON pm.id = b.provider_model_id
WHERE pm.raw_model_name ILIKE '%glm-5.2%'
  AND pm.outbound_model_name = 'glm-5.1'
ORDER BY b.credential_id;

-- 1.3 排除项（应返回 0 行）：model_aliases 表里不应该有 glm-5.2
\echo '--- 1.3 model_aliases 中是否存在 glm-5.2 的 alias 行（预期 0） ---'
SELECT COUNT(*) AS glm52_alias_count
FROM model_aliases
WHERE raw_name ILIKE '%glm-5.2%';

\echo '========================================================'
\echo ' 第 2 部分：DRY-RUN 预览（只读）'
\echo '========================================================'

-- 2.1 显示修复后候选 cand.RawModel 的变化
--     修复前：COALESCE('glm-5.1','glm-5.2') = 'glm-5.1'  ← 错误
--     修复后：COALESCE(NULL,    'glm-5.2') = 'glm-5.2'  ← 正确
\echo '--- 2.1 模拟修复后的 COALESCE 行为（应全部返回 glm-5.2） ---'
SELECT
    pm.id,
    pm.raw_model_name,
    COALESCE(NULL, pm.raw_model_name) AS fixed_outbound_model
FROM provider_models pm
WHERE pm.raw_model_name ILIKE '%glm-5.2%'
  AND pm.outbound_model_name = 'glm-5.1'
ORDER BY pm.id;

\echo '========================================================'
\echo ' 第 3 部分：修复（DML，请人工确认后取消注释执行）'
\echo '========================================================'

-- 执行前请先备份：
--   docker exec pg-252-pg17 pg_dump -U llm_gateway -d llm_gateway \
--     -t provider_models -t credential_model_bindings \
--     --data-only --inserts > /root/backup_pm_$(date +%Y%m%d_%H%M).sql

-- BEGIN;
--
-- -- 3.1 把 glm-5.2 的 outbound_model_name 清空
-- UPDATE provider_models
-- SET outbound_model_name = NULL,
--     updated_at = now()
-- WHERE raw_model_name ILIKE '%glm-5.2%'
--   AND outbound_model_name = 'glm-5.1';
--
-- -- 3.2 复核：应返回 0 行
-- SELECT COUNT(*)
-- FROM provider_models
-- WHERE raw_model_name ILIKE '%glm-5.2%'
--   AND outbound_model_name = 'glm-5.1';
--
-- -- 3.3 复核：候选 cand.RawModel 不再是 glm-5.1
-- SELECT pm.id, pm.raw_model_name,
--        COALESCE(pm.outbound_model_name, pm.raw_model_name) AS cand_raw_model
-- FROM provider_models pm
-- WHERE pm.raw_model_name ILIKE '%glm-5.2%'
-- ORDER BY pm.id;
--
-- -- 确认无误：COMMIT;   否则：ROLLBACK;
-- COMMIT;

\echo '修复脚本结束。COMMIT 后 30-120s 候选缓存自动失效（resolve/resolve.go:38-49），'
\echo '之后用 model=glm-5.2 重新请求应能看到上游收到 glm-5.2。'
