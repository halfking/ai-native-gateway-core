-- 867_session_summaries_last_request_at_idx.down.sql
-- 回滚：按名删 867 索引（普通表单索引，无级联），不动数据。
DROP INDEX IF EXISTS public.idx_session_summaries_last_request_recent;
