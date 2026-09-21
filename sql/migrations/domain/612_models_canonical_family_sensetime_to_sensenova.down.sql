-- Migration 612 DOWN: reverse family rename + drop 4 API SKU
-- Date: 2026-08-29
--
-- 用法（紧急回滚用，生产慎跑）：
--   psql $DATABASE_URL -v ON_ERROR_STOP=1 \
--     -f sql/migrations/domain/612_models_canonical_family_sensetime_to_sensenova.down.sql
--
-- 注意：
--   1. down 只把 family 改回 'sensetime'，不恢复原 ctx 值（已写为 NULL 的
--      sensenova-xl 若曾被 612 兜底成 524288，会被改回 NULL）。
--   2. dump-standard-models.sh 重生成 .sql 快照时会再次覆盖；
--      本文件只是紧急回滚。
--   3. 不动 model_families / provider_catalog / 02-seed.sql 基线。

BEGIN;

-- 1. 删除 4 条 API 验证 SKU
DELETE FROM models_canonical
WHERE  canonical_name IN (
         'sensenova-6.7-flash-lite',
         'sensenova-6.8-flash-lite',
         'sensenova-u1-fast',
         'sensenova-u1.5-lite'
       );

-- 2. 还原 sensenova-xl 的 context_window 为 NULL（若曾被 612 兜底过）
UPDATE models_canonical
SET    context_window        = NULL,
       context_window_source = 'catalog',
       modality              = COALESCE(modality, 'text'),
       notes                 = NULL,
       updated_at            = NOW()
WHERE  canonical_name        = 'sensenova-xl';

-- 3. family 回滚
UPDATE models_canonical
SET    family     = 'sensetime',
       updated_at = NOW()
WHERE  family     = 'sensenova'
  AND  canonical_name IN (
        'sensechat-5',
        'sensechat-5-thinking',
        'sensechat-turbo',
        'sensenova-xl'
       );

SELECT pg_notify('auto_route_refresh', 'manual:612:down');

COMMIT;
