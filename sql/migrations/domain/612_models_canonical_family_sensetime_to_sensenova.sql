-- Migration 612: normalize family 'sensetime' -> 'sensenova' for SenseNova / SenseTime
--                 + upsert 4 API-verified SKU into models_canonical
-- Date: 2026-08-29
-- Author: zcode
--
-- Purpose:
--   1. 把 models_canonical 中历史 family='sensetime' 的 4 行（sensechat-5 /
--      sensechat-5-thinking / sensechat-turbo / sensenova-xl）统一改为
--      'sensenova'，与 provider_catalog / providers / model_families /
--      migration 333 CASE / discovery/normalize.go 全部对齐。
--   2. UPSERT 商汤官方 /v1/models（已用凭据实测 HTTP 200）当前白名单
--      内的 4 个原厂 SKU:
--        - sensenova-6.7-flash-lite   (text + image in / text out, reasoning, 262144 ctx)
--        - sensenova-6.8-flash-lite   (text + image in / text out, reasoning, 262144 ctx)
--        - sensenova-u1-fast          (text in / image out, reasoning, 262144 ctx)
--        - sensenova-u1.5-lite        (text in / image out, reasoning, 262144 ctx)
--   3. 把 sensenova-xl 的 context_window 从 NULL 兜底为 524288（512K），
--      标记 context_window_source='manual' 留 audit 痕迹。
--
-- Source authenticity:
--   - 4 条 API 行：2026-08-29 实测 token.sensenova.cn/v1/models 返回，
--     model id / ctx / input_modalities / output_modalities / supported_features
--     全部经 key 验证。provider 用 sensenova 自身的 model spec。
--   - 4 条历史 seed 行：来源 02-seed.sql / provider_catalog manifest；
--     ctx 沿用现状（131072 / 131072 / 32768 / NULL -> 524288 兜底）。
--
-- 不在本迁移范围（缺权威字段，留待后续）：
--   - SenseNova U1 Pro / U1 (基线版) / Vision / MARS / SI
--   - SenseChat-Image / Shinobi 视频 / SenseChat-Reasoning 旧名
--   - 6.8 Flash Lite（产品页冠名；本轮以 API 列出的 'sensenova-6.8-flash-lite'
--     为准，不另设别名；后续若商汤官方另开 6.9 / 7.0 等再加）
--
-- Compatibility:
--   - 全部使用 ON CONFLICT / WHERE 守卫，幂等可重复跑。
--   - 仅当 family='sensetime' 时才覆盖，避免误改其它 family 字符串。
--   - sensenova-xl 的 524288 兜底只在 context_window IS NULL 时执行，
--     不踩运营已填值。
--   - 不动 model_families.code（已为 sensenova）、provider_catalog.code、
--     三份 02-seed.sql 基线。

BEGIN;

-- ============================================================
-- 1. family rename: sensetime -> sensenova（限定 4 个 canonical_name）
-- ============================================================
UPDATE models_canonical
SET    family      = 'sensenova',
       updated_at  = NOW()
WHERE  family      = 'sensetime'
  AND  canonical_name IN (
        'sensechat-5',
        'sensechat-5-thinking',
        'sensechat-turbo',
        'sensenova-xl'
       );

-- ============================================================
-- 2. UPSERT 4 条商汤官方 /v1/models API 已验证的 SKU
--    字段顺序与 dump-standard-models.sh 输出列对齐：
--    canonical_name, family, parameters_b, modality, context_window,
--    multimodal_caps, context_window_override, context_window_source,
--    status, source, released_at, version_rank, complexity_ceiling,
--    display_name, notes, created_at, updated_at
-- ============================================================
INSERT INTO models_canonical (
    canonical_name, family, parameters_b, modality, context_window,
    multimodal_caps, context_window_override, context_window_source,
    status, source, released_at, version_rank, complexity_ceiling,
    display_name, notes, created_at, updated_at
) VALUES
    -- sensenova-6.7-flash-lite：text + image in / text out, reasoning
    ('sensenova-6.7-flash-lite', 'sensenova', NULL, 'multimodal', 262144,
     ARRAY['image']::text[], NULL, 'catalog',
     'active', 'seed', NULL, 2, 'medium',
     '商汤 SenseNova 6.7 Flash-Lite',
     'SenseNova 6.7 Flash-Lite (lightweight multimodal agent, text+image in / text out, supports reasoning, 256K ctx). API 验证 token.sensenova.cn/v1/models 2026-08-29.',
     NOW(), NOW()),

    -- sensenova-6.8-flash-lite：text + image in / text out, reasoning
    ('sensenova-6.8-flash-lite', 'sensenova', NULL, 'multimodal', 262144,
     ARRAY['image']::text[], NULL, 'catalog',
     'active', 'seed', NULL, 1, 'medium',
     '商汤 SenseNova 6.8 Flash-Lite',
     'SenseNova 6.8 Flash-Lite (lightweight multimodal agent, text+image in / text out, supports reasoning, 256K ctx, version_rank=1 最新). API 验证 token.sensenova.cn/v1/models 2026-08-29.',
     NOW(), NOW()),

    -- sensenova-u1-fast：text in / image out (信息图生成加速版)
    ('sensenova-u1-fast', 'sensenova', NULL, 'multimodal', 262144,
     ARRAY['image_out']::text[], NULL, 'catalog',
     'active', 'seed', NULL, 1, 'medium',
     '商汤 SenseNova U1 Fast',
     'SenseNova U1 Fast (U1 加速版, infographics 生成, text in / image out, supports reasoning, 256K ctx). API 验证 token.sensenova.cn/v1/models 2026-08-29.',
     NOW(), NOW()),

    -- sensenova-u1.5-lite：text in / image out (信息图生成加速版)
    ('sensenova-u1.5-lite', 'sensenova', NULL, 'multimodal', 262144,
     ARRAY['image_out']::text[], NULL, 'catalog',
     'active', 'seed', NULL, 1, 'medium',
     '商汤 SenseNova U1.5 Lite',
     'SenseNova U1.5 Lite (U1.5 加速版, infographics 生成, text in / image out, supports reasoning, 256K ctx). API 验证 token.sensenova.cn/v1/models 2026-08-29.',
     NOW(), NOW())
ON CONFLICT (canonical_name) DO UPDATE SET
    family               = EXCLUDED.family,
    modality             = EXCLUDED.modality,
    context_window       = EXCLUDED.context_window,
    multimodal_caps      = EXCLUDED.multimodal_caps,
    context_window_source= EXCLUDED.context_window_source,
    display_name         = EXCLUDED.display_name,
    notes                = EXCLUDED.notes,
    version_rank         = EXCLUDED.version_rank,
    complexity_ceiling   = EXCLUDED.complexity_ceiling,
    status               = EXCLUDED.status,
    updated_at           = NOW();

-- ============================================================
-- 3. sensenova-xl 兜底 512K ctx（仅当 NULL 时，避免踩运营已填值）
-- ============================================================
UPDATE models_canonical
SET    context_window        = 524288,
       context_window_source = 'manual',
       modality              = COALESCE(modality, 'text'),
       display_name          = COALESCE(display_name, '商汤 SenseNova XL'),
       notes                 = COALESCE(notes, 'SenseNova XL (历史 seed, 2026-08-29 上下文 512K 兜底, 未在当前 /v1/models 白名单).'),
       updated_at            = NOW()
WHERE  canonical_name        = 'sensenova-xl'
  AND  context_window IS NULL;

-- ============================================================
-- 4. 触发 auto_route_refresh，让 routing 立即感知 family 变更
-- ============================================================
SELECT pg_notify('auto_route_refresh', 'manual:612');

COMMIT;
