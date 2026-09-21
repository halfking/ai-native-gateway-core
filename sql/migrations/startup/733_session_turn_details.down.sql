-- Migration 733 down: 撤销 session_turn_details 表族。
-- 前置：734 视图已先回滚（down 按号逆序），否则视图引用本表会 2BP01。

BEGIN;

DROP FUNCTION IF EXISTS public.promote_session_turn_details_hot_to_partition(INTERVAL, INTEGER);
DROP FUNCTION IF EXISTS public.ensure_session_turn_details_partition(date);

DROP TABLE IF EXISTS public.session_turn_details_hot;
DROP TABLE IF EXISTS public.session_turn_details;  -- 分区 CASCADE 由依赖清理

DROP SEQUENCE IF EXISTS public.session_turn_details_id_seq;

COMMIT;
