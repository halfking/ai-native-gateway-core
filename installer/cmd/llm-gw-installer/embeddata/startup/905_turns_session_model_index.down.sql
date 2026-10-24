-- Migration 905 (down): 删掉 905 加的两条索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么这样删
-- ═══════════════════════════════════════════════════════════════════════
--
-- · idx_session_turns_model_session 是**分区父表**上的索引 ⇒ 按名 DROP 父表索引
--   会级联删掉各子分区侧子索引（PG 按 pg_index.indisparent 关联，**不按名字 grep**
--   —— 877 台账口径：分区侧子索引会被 PG 按子表名自动改名）。
-- · idx_session_turns_hot_model_session 是 hot 表上的普通索引 ⇒ 按名删即可。
--
-- ★ 回滚代价：删这两条索引会让 `?model=` 这类**稀有**过滤回到
--   「外层扫 35 万个会话」的生产形态（实测 2026-10-24：500@30s）。
--   ⇒ 删索引前应先回滚二进制（回到 R90 形态 = 17.5s 形态，
--   实测仍比 500 好），否则窗口期内该过滤路径会超时。
--
-- 【幂等】IF EXISTS。重复执行不报错。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_session_turns_model_session;
DROP INDEX IF EXISTS public.idx_session_turns_hot_model_session;

COMMIT;

-- POST_CONDITION:
--   SELECT count(*) FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_turns_model_session',
--                         'idx_session_turns_hot_model_session');
--   -- 预期：0
