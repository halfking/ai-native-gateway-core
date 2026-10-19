-- 877 down：删掉父壳与 hot 侧索引（删父表分区索引会级联脱离各分区子索引），
-- 并按 640 原样重建被删掉的 upstream_protocol 索引。不动数据。
--
-- 与 up 同款：删父壳本身很快（只是解除 ATTACH 关系并 DROP 各个分区索引），
-- 但**重建** upstream_protocol 那条、以及未来重跑 877，都要重新抽 trigram，
-- 属分钟级。回滚不是轻量操作。
--
-- ⚠ 本文件**不需要** installer 的非事务豁免标记（718 那种）：三条语句都不是
--   CONCURRENTLY，在 --single-transaction 下也能原子执行，保留事务是有意为之。
--   （此处刻意不写出该标记的字面量——dbinit 目录那道门按"注释里出现标记字样"
--   判定，写出来解释反而会被算成声明豁免。）
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_session_turns_search_trgm;
DROP INDEX IF EXISTS public.idx_session_turns_hot_search_trgm;

-- 877 删掉的 640 索引，回滚时按 640 原样重建（tenant_id, upstream_protocol, ts DESC）。
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_upstream_protocol
    ON public.session_turns_hot (tenant_id, upstream_protocol, ts DESC)
    WHERE upstream_protocol IS NOT NULL;

COMMIT;
