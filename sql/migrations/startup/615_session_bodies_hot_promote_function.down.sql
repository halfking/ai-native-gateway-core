-- Rollback: 615_session_bodies_hot_promote_function

DROP FUNCTION IF EXISTS public.promote_session_bodies_hot_to_partition(interval, integer);
