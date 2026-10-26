-- 874 down：按名删父级索引（级联删分区子索引），不动数据。
BEGIN;

SET LOCAL lock_timeout = '60s';

DROP INDEX IF EXISTS public.idx_session_turns_client_request_id;

COMMIT;
