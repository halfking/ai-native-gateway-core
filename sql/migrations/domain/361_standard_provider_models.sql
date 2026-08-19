-- 361_standard_provider_models.sql
-- Phase: 标准 provider_models 行预填（xai / moonshot / google-gemini）
-- Idempotent: ON CONFLICT (provider_id, raw_model_name) DO UPDATE 只刷新可热改字段。
--
-- 背景：
--   provider_models 是网关内部"该 provider 暴露哪些原始模型"的目录。
--   此前该表由模型发现（domains/discovery 调用 /models 端点）冷启动填充，
--   缺乏 provider key 时不出现。三个目标 provider 当前均为 0 凭据，
--   因此 provider_models 也是 0 行 → 用户在管理后台看不到 grok-4.6 / kimi / gemini-3.*。
--   本迁移预填"标准行"——available=true，outbound=raw，canonical_id 指向 352-355 写入的
--   models_canonical 行；当对应 credential 接入后，由模型发现流程覆盖 last_seen_at 与
--   任何远程探测到的属性（ctx、display_name 等）。
--
-- 设计原则：
--   - 仅预填；不替代发现流程
--   - 不触碰 credentials / bindings（由 362 处理）
--   - 不修改 352-358 / 478 既有 canonical / alias 行

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────
-- xai (providers.id = 30): grok-4.6
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 30, 'default', 'grok-4.6', mc.id, 'grok-4.6',
       'grok-4.6', 'grok-4.6', 'vision', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'grok-4.6'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

-- ─────────────────────────────────────────────────────────────────────────
-- moonshot (providers.id = 17): kimi-k3 / kimi-k2.6 / kimi-k2.7-code / kimi-k2.7-code-highspeed
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 17, 'default', 'kimi-k3', mc.id, 'kimi-k3',
       'kimi-k3', 'kimi-k3', 'multimodal', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'kimi-k3'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 17, 'default', 'kimi-k2.6', mc.id, 'kimi-k2.6',
       'kimi-k2.6', 'kimi-k2.6', 'vision', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'kimi-k2.6'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 17, 'default', 'kimi-k2.7-code', mc.id, 'kimi-k2.7-code',
       'kimi-k2.7-code', 'kimi-k2.7-code', 'text', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'kimi-k2.7-code'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 17, 'default', 'kimi-k2.7-code-highspeed', mc.id, 'kimi-k2.7-code-highspeed',
       'kimi-k2.7-code-highspeed', 'kimi-k2.7-code-highspeed', 'text', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.canonical_name = 'kimi-k2.7-code-highspeed'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

-- ─────────────────────────────────────────────────────────────────────────
-- google-gemini (providers.id = 10): gemini-3.* (355 写入的全部 10 个)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, last_seen_at, updated_at
)
SELECT 10, 'default', mc.canonical_name, mc.id, mc.canonical_name,
       mc.canonical_name, mc.canonical_name, 'multimodal', true, NOW(), NOW()
FROM models_canonical mc
WHERE mc.family = 'google-gemini'
  AND mc.canonical_name LIKE 'gemini-3%'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    canonical_raw_name = EXCLUDED.canonical_raw_name,
    standardized_name = EXCLUDED.standardized_name,
    outbound_model_name = EXCLUDED.outbound_model_name,
    modality = EXCLUDED.modality,
    available = EXCLUDED.available,
    last_seen_at = NOW(),
    updated_at = NOW();

COMMIT;