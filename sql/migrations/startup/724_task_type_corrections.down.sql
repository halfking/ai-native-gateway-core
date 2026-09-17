-- 721_task_type_corrections.down.sql
-- Reverse of 721: drop the taskprofile corrections table.
-- Human corrections are review data — operators should pg_dump this table
-- before running the down migration outside a rollback drill.

BEGIN;

DROP TABLE IF EXISTS public.task_type_corrections;

COMMIT;
