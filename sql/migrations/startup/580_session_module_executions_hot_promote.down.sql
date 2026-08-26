-- Down for 580: remove the session_module_executions hot-promote function and
-- its lifecycle setting. The hot table and partitioned parent predate this
-- migration and are left untouched.

\set ON_ERROR_STOP on

BEGIN;

DROP FUNCTION IF EXISTS public.promote_session_module_executions_hot_to_partition(interval, integer);

DELETE FROM public.settings_kv
 WHERE key = 'lifecycle.session_module_executions_hot_retention_hours'
   AND scope = 'platform';

COMMIT;
