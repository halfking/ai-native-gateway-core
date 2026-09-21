-- Migration 613: 修复 glm-5.2 的 outbound_model_name 漂移（静默降级根因）
-- Date: 2026-08-29
-- Author: zcode
--
-- Purpose:
--   修复 GLM-5.2 通过网关被"静默降级"的根因 —— provider_models.outbound_model_name
--   被错误填成 'glm-5.1'，导致网关把客户端请求的 model:"glm-5.2" 改写成
--   model:"glm-5.1" 转发给上游，用户实际拿到的是降级后的 glm-5.1。
--
--   请求路径（与 sql/fixes/fix-glm52-alias-drift.sql 记载的真实事故一致）：
--     client request: model = "glm-5.2"
--     provider/client.go: loadCandidatesByModalityDB 命中 raw_model_name='glm-5.2'
--       → cand.RawModel = COALESCE(outbound_model_name, raw_model_name)
--         (provider/client.go:911, :1322)
--       → 因为 outbound_model_name='glm-5.1'，cand.RawModel='glm-5.1'
--     executor_chat.go: resolveOutboundModel / prepareRequestBody
--       → outboundModel != clientModel 时 replaceModelInRequestBody 把 body 里
--         "model":"glm-5.2" 替换为 "model":"glm-5.1"
--     上游供应商实际收到 glm-5.1
--
--   直连时客户端直接写 model:"glm-5.2"，绕过了这层 COALESCE 改写，所以正常。
--
-- 修复策略：把 glm-5.2 的 outbound_model_name 清空为 NULL，让 COALESCE 兜底
--           走 raw_model_name='glm-5.2'，请求原样转发给供应商。
--           上游 zhipu / 类 zhipu 清单已含 'glm-5.2'，字面直发被接受
--           （见 fix-glm52-alias-drift.sql 的对照组说明）。
--
--   仅匹配 outbound_model_name = 'glm-5.1'（已证实的漂移值），不动 NIM 那类
--   合法的发布商前缀 'z-ai/glm-5.2'（其 outbound 不含 'glm-5.1'）。
--
--   若现场 sp1/spi-3 的漂移值不是精确的 'glm-5.1'（例如其它错误别名），
--   请先跑第 1 部分只读 SELECT 确认实际 outbound_model_name，再按需调整
--   第 2 部分的 WHERE 条件。
--
-- 用法：
--   psql $DATABASE_URL -v ON_ERROR_STOP=1 \
--     -f sql/migrations/domain/613_glm52_outbound_model_name_drift_fix.sql
--
-- 幂等：WHERE 守卫确保已为 NULL 的行不会被重复更新；可重复安全执行。

BEGIN;

-- ============================================================
-- 1. 检测（只读）：列出将被清空的 provider_models 行
-- ============================================================
\echo '--- 1. provider_models 中 raw_model_name 含 glm-5.2 且 outbound_model_name = glm-5.1 ---'
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

-- ============================================================
-- 2. 修复：清空 outbound_model_name 为 NULL，COALESCE 兜底走 raw_model_name
-- ============================================================
\echo '--- 2. 清空 glm-5.2 漂移行的 outbound_model_name ---'
UPDATE provider_models
SET    outbound_model_name = NULL,
       updated_at          = NOW()
WHERE  raw_model_name ILIKE '%glm-5.2%'
  AND  outbound_model_name = 'glm-5.1';

-- ============================================================
-- 3. 触发 auto_route_refresh，让网关重载候选（cand.RawModel 重新计算）
-- ============================================================
SELECT pg_notify('auto_route_refresh', 'manual:613');

COMMIT;
