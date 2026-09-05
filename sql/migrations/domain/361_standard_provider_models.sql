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
--   - 通过 providers.code 解析 provider_id，避免硬编码 id（环境无关）
--   - source 列标记 'migration-361'，便于 361.down 精确清理（不清发现写入的行）
--
-- 审计修复（feat/standard-models-rollout 2026-08-20）：
--   - 改为 SELECT id FROM providers WHERE code = ... AND tenant_id = 'default'
--   - UPDATE 不再覆盖 last_seen_at（避免误清发现 worker 的真实时间戳）
--   - 不再 UPDATE canonical_raw_name / standardized_name / outbound_model_name（值不变）
--   - 写入 source 列（新增列前以 NOT NULL DEFAULT 'discovery' 兼容旧 DB）

BEGIN;

-- 防御性 ALTER：source 列若不存在则添加。DEFAULT 'discovery' 兼容旧 provider_models 行。
ALTER TABLE provider_models
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'discovery';

-- ─────────────────────────────────────────────────────────────────────────
-- xai (providers.code = 'xai'): grok-4.6
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, source,
    last_seen_at, updated_at
)
SELECT p.id, 'default', 'grok-4.6', mc.id, 'grok-4.6',
       'grok-4.6', 'grok-4.6', 'vision', true, 'migration-361',
       NOW(), NOW()
FROM providers p
JOIN models_canonical mc ON mc.canonical_name = 'grok-4.6'
WHERE p.code = 'xai' AND p.tenant_id = 'default'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    modality = EXCLUDED.modality,
    source = EXCLUDED.source,
    updated_at = NOW();
-- 故意不更新 last_seen_at / canonical_raw_name / standardized_name / outbound_model_name /
-- available：发现 worker 写入这些字段时应保持其值；361 仅在 INSERT 时设定它们。

-- ─────────────────────────────────────────────────────────────────────────
-- moonshot (providers.code = 'moonshot'): kimi-k3 / kimi-k2.6 / kimi-k2.7-code / kimi-k2.7-code-highspeed
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, source,
    last_seen_at, updated_at
)
SELECT p.id, 'default', mc.canonical_name, mc.id, mc.canonical_name,
       mc.canonical_name, mc.canonical_name,
       CASE mc.canonical_name
           WHEN 'kimi-k3'                  THEN 'multimodal'
           WHEN 'kimi-k2.6'                THEN 'vision'
           ELSE 'text'
       END,
       true, 'migration-361',
       NOW(), NOW()
FROM providers p
JOIN models_canonical mc
  ON mc.canonical_name IN ('kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed')
WHERE p.code = 'moonshot' AND p.tenant_id = 'default'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    modality = EXCLUDED.modality,
    source = EXCLUDED.source,
    updated_at = NOW();

-- ─────────────────────────────────────────────────────────────────────────
-- google-gemini (providers.code = 'google-gemini'): gemini-3.* (355 写入的全部 10 个)
-- ─────────────────────────────────────────────────────────────────────────
INSERT INTO provider_models (
    provider_id, tenant_id, raw_model_name, canonical_id, canonical_raw_name,
    standardized_name, outbound_model_name, modality, available, source,
    last_seen_at, updated_at
)
SELECT p.id, 'default', mc.canonical_name, mc.id, mc.canonical_name,
       mc.canonical_name, mc.canonical_name, 'multimodal', true, 'migration-361',
       NOW(), NOW()
FROM providers p
JOIN models_canonical mc
  ON mc.family = 'google-gemini' AND mc.canonical_name LIKE 'gemini-3%'
WHERE p.code = 'google-gemini' AND p.tenant_id = 'default'
ON CONFLICT (provider_id, raw_model_name) DO UPDATE
SET canonical_id = EXCLUDED.canonical_id,
    modality = EXCLUDED.modality,
    source = EXCLUDED.source,
    updated_at = NOW();

COMMIT;