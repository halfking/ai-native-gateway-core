-- Migration 613 DOWN: 还原 glm-5.2 的 outbound_model_name 漂移（紧急回滚用）
-- Date: 2026-08-29
--
-- 用法（紧急回滚用，生产慎跑）：
--   psql $DATABASE_URL -v ON_ERROR_STOP=1 \
--     -f sql/migrations/domain/613_glm52_outbound_model_name_drift_fix.down.sql
--
-- 注意：
--   本文件是 613 的反向操作，会把 glm-5.2 的 outbound_model_name 重新填回
--   'glm-5.1'（即重新引入"静默降级"），仅用于需要回滚 613 的紧急场景。
--   重跑 dump-standard-models.sh 生成的快照会再次覆盖；本文件只是紧急回滚。

BEGIN;

UPDATE provider_models
SET    outbound_model_name = 'glm-5.1',
       updated_at          = NOW()
WHERE  raw_model_name ILIKE '%glm-5.2%'
  AND  outbound_model_name IS NULL;

SELECT pg_notify('auto_route_refresh', 'manual:613:down');

COMMIT;
