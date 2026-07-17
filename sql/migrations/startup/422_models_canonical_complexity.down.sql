-- 422_models_canonical_complexity.down.sql
-- 回滚 422：删除 models_canonical 的复杂度列。
-- 安全：列默认 NULL，NULL = 不参与过滤，回滚后 auto 路由的复杂度维度失效，
-- Decider 回退到 8 维评分（auto_complexity_score flag 会因列缺失而拒绝开启）。

BEGIN;

DROP INDEX IF EXISTS public.idx_models_canonical_complexity_ceiling;
ALTER TABLE models_canonical DROP COLUMN IF EXISTS min_complexity;
ALTER TABLE models_canonical DROP COLUMN IF EXISTS complexity_ceiling;

COMMIT;
