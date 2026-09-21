-- 693_provider_models_canonical_cleared_at.down.sql
-- 回滚管理员解绑标记列。列是唯一变更对象，无视图/触发器依赖，直接删除即可。

BEGIN;

ALTER TABLE public.provider_models DROP COLUMN IF EXISTS canonical_cleared_at;

DELETE FROM public.schema_migrations WHERE version = '693';

COMMIT;
