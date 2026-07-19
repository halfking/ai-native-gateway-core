-- ===========================================================================
-- File:          sql/migrations/startup/446_volcengine_model_aliases.sql
-- Database:      llm_gateway
-- Purpose:       修复火山方舟普通版的模型别名映射
-- Status:        active
-- Idempotent:    YES (NOT EXISTS / ON CONFLICT)
-- Rollback:      手工删除本迁移新增的 alias；provider_catalog 回滚需使用备份
-- Changelog:
--   2026-07-19  v1.0  Add verified Volcano model aliases and catalog mapping
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- Canonical rows must exist before alias INSERT ... SELECT statements run.
INSERT INTO models_canonical (canonical_name, status, created_at, updated_at)
VALUES
    ('doubao-seed-2-0-code-preview-260215', 'active', NOW(), NOW()),
    ('doubao-seed-2-0-pro-260215', 'active', NOW(), NOW()),
    ('doubao-seed-2-0-lite-260428', 'active', NOW(), NOW()),
    ('doubao-seed-2-0-mini-260428', 'active', NOW(), NOW()),
    ('deepseek-v4-pro-260425', 'active', NOW(), NOW()),
    ('deepseek-v4-flash-260425', 'active', NOW(), NOW()),
    ('glm-5-2-260617', 'active', NOW(), NOW())
ON CONFLICT (canonical_name) DO UPDATE SET
    status = 'active',
    updated_at = NOW();

-- 修复火山方舟普通版的模型别名映射
-- 问题：客户端使用简短名称（如 doubao-seed-code），但火山 API 要求完整版本号（如 doubao-seed-2-0-code-preview-260215）

-- 1. 添加缺失的模型别名映射（简短名 -> 带版本号的完整名）

-- doubao-seed-code 系列
INSERT INTO public.model_aliases (canonical_id, raw_name, status)
SELECT
    mc.id,
    'doubao-seed-code',
    'active'
FROM models_canonical mc
WHERE mc.canonical_name = 'doubao-seed-2-0-code-preview-260215'
  AND NOT EXISTS (
      SELECT 1 FROM public.model_aliases ma
      WHERE ma.raw_name = 'doubao-seed-code' AND ma.canonical_id = mc.id
  );

-- doubao-seed-2.0-code -> doubao-seed-2-0-code-preview-260215
INSERT INTO public.model_aliases (canonical_id, raw_name, status)
SELECT
    mc.id,
    'doubao-seed-2.0-code',
    'active'
FROM models_canonical mc
WHERE mc.canonical_name = 'doubao-seed-2-0-code-preview-260215'
  AND NOT EXISTS (
      SELECT 1 FROM public.model_aliases ma
      WHERE ma.raw_name = 'doubao-seed-2.0-code' AND ma.canonical_id = mc.id
  );

-- minimax-m2.7 (如果有对应的 canonical)
-- 注意：需要确认火山是否有 minimax 的对应版本号模型

-- glm-5.1 -> glm-5-2-260617 (根据实测可用模型)
INSERT INTO public.model_aliases (canonical_id, raw_name, status)
SELECT
    mc.id,
    'glm-5.1',
    'active'
FROM models_canonical mc
WHERE mc.canonical_name = 'glm-5-2-260617'
  AND NOT EXISTS (
      SELECT 1 FROM public.model_aliases ma
      WHERE ma.raw_name = 'glm-5.1' AND ma.canonical_id = mc.id
  );

-- kimi-k2.6 -> kimi-k2-thinking-251104 (但实测 404，需要确认)
-- 暂时注释掉，因为实测这个模型在火山不可用
-- INSERT INTO public.model_aliases (canonical_id, raw_name, status)
-- SELECT mc.id, 'kimi-k2.6', 'active'
-- FROM models_canonical mc
-- WHERE mc.canonical_name = 'kimi-k2-thinking-251104'
-- ON CONFLICT (raw_name, canonical_id) DO NOTHING;

-- deepseek-v4-pro -> deepseek-v4-pro-260425
INSERT INTO public.model_aliases (canonical_id, raw_name, status)
SELECT
    mc.id,
    'deepseek-v4-pro',
    'active'
FROM models_canonical mc
WHERE mc.canonical_name = 'deepseek-v4-pro-260425'
  AND NOT EXISTS (
      SELECT 1 FROM public.model_aliases ma
      WHERE ma.raw_name = 'deepseek-v4-pro' AND ma.canonical_id = mc.id
  );

-- deepseek-v4-flash -> deepseek-v4-flash-260425
INSERT INTO public.model_aliases (canonical_id, raw_name, status)
SELECT
    mc.id,
    'deepseek-v4-flash',
    'active'
FROM models_canonical mc
WHERE mc.canonical_name = 'deepseek-v4-flash-260425'
  AND NOT EXISTS (
      SELECT 1 FROM public.model_aliases ma
      WHERE ma.raw_name = 'deepseek-v4-flash' AND ma.canonical_id = mc.id
  );

-- 2. 更新 provider_catalog 中的模型列表，使用正确的完整版本号
-- 注意：provider_catalog 实际列名是 code / models_manifest_json，
--   历史迁移脚本误用 slug / supported_models 已修正
UPDATE provider_catalog
SET models_manifest_json = '[
  {"id": "doubao-seed-2-0-code-preview-260215", "ctx_k": 128, "display_name": "Doubao Seed Code"},
  {"id": "doubao-seed-2-0-pro-260215", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Pro"},
  {"id": "doubao-seed-2-0-lite-260428", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Lite"},
  {"id": "doubao-seed-2-0-mini-260428", "ctx_k": 128, "display_name": "Doubao Seed 2.0 Mini"},
  {"id": "deepseek-v4-pro-260425", "ctx_k": 64, "display_name": "DeepSeek V4 Pro"},
  {"id": "deepseek-v4-flash-260425", "ctx_k": 64, "display_name": "DeepSeek V4 Flash"},
  {"id": "glm-5-2-260617", "ctx_k": 128, "display_name": "GLM-5.2"},
  {"id": "glm-4-7-251222", "ctx_k": 128, "display_name": "GLM-4.7"}
]'::jsonb,
    updated_at = NOW()
WHERE code = 'volcengine-coding';

COMMIT;
