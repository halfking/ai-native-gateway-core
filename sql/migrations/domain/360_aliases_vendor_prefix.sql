-- 360_aliases_vendor_prefix.sql
-- Phase: 标准模型的 vendor-prefix 别名补齐
-- Idempotent: ON CONFLICT (canonical_id, raw_name) DO NOTHING.
--
-- 背景：
--   352/354/355 已为 grok-4.6 / kimi-k3/k2.6/k2.7 / gemini-3.* 写入
--   canonical_name + 一条 self-alias（raw_name = canonical_name）。
--   但缺少下游 OpenAI 兼容网关常见的 vendor-prefix 别名，例如：
--     - "openai/grok-4.6" 风格（OpenRouter 透传）
--     - "google/gemini-3.5-flash" 风格（Vertex / AI Studio）
--     - "moonshot-v1/kimi-k3" / "kimi-k3-0528" 等带日期的版本戳
--     - 旧 dash 形式 "grok-4-6"（OpenAI 旧版客户端解析）
--   这些别名让上游凭据在 OpenRouter / vLLM / 本地代理等场景下能被网关正确归一。
--
-- 注意：
--   - 不得改动 352/354/355/357/358 既有别名行
--   - 仅新增；与既有 raw_name 冲突时由 357 的 (canonical_id, raw_name) 唯一约束保护

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- grok-4.6
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'openai/grok-4.6', 'active', 'OpenRouter-style vendor prefix', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'grok-4.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'grok-4-6', 'active', 'Legacy dash form (OpenAI old client)', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'grok-4.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────
-- kimi-k3 / kimi-k2.6 / kimi-k2.7-code / kimi-k2.7-code-highspeed
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'moonshot-v1/kimi-k3', 'active', 'Moonshot v1 path alias', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k3'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'kimi-k3-0528', 'active', 'Kimi k3 dated snapshot (0528)', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k3'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'moonshot-v1/kimi-k2.6', 'active', 'Moonshot v1 path alias', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k2.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'kimi-k2-6', 'active', 'Legacy dash form', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k2.6'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'moonshot-v1/kimi-k2.7-code', 'active', 'Moonshot v1 path alias', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k2.7-code'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'moonshot-v1/kimi-k2.7-code-highspeed', 'active', 'Moonshot v1 path alias', NOW(), NOW()
FROM models_canonical WHERE canonical_name = 'kimi-k2.7-code-highspeed'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────
-- gemini-3.* (355 写入的 10 个 canonical)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'google/' || canonical_name, 'active', 'Google AI Studio path prefix', NOW(), NOW()
FROM models_canonical
WHERE family = 'google-gemini' AND canonical_name LIKE 'gemini-3%'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

INSERT INTO model_aliases (canonical_id, raw_name, status, notes, created_at, updated_at)
SELECT id, 'models/' || canonical_name, 'active', 'Vertex AI models/ path prefix', NOW(), NOW()
FROM models_canonical
WHERE family = 'google-gemini' AND canonical_name LIKE 'gemini-3%'
ON CONFLICT (canonical_id, raw_name) DO NOTHING;

COMMIT;