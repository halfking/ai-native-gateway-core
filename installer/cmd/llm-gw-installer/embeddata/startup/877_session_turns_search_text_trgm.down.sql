-- 877 down：按名删两条父级/hot 级索引（删父表索引级联删各分区子索引），不动数据。
--
-- ⚠ 回滚代价：重建需要重新对全部 search_text 抽 trigram（6.4GB 级分区属
--   分钟级），且期间写被 ShareLock 阻塞。不要把这条当轻量操作。
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_session_turns_search_trgm;
DROP INDEX IF EXISTS public.idx_session_turns_hot_search_trgm;

-- 877 删掉的 640 索引，回滚时按 640 原样重建。
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_upstream_protocol
    ON public.session_turns_hot (tenant_id, upstream_protocol, ts DESC)
    WHERE upstream_protocol IS NOT NULL;

COMMIT;
