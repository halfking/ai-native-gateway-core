-- Down for 579: remove the dashboard_access_events hot-promote function and
-- its lifecycle setting. The hot table and partitioned parent predate this
-- migration and are left untouched.

\set ON_ERROR_STOP on

BEGIN;

DROP FUNCTION IF EXISTS public.promote_dashboard_access_events_hot_to_partition(interval, integer);

DELETE FROM public.settings_kv
 WHERE key = 'lifecycle.dashboard_access_events_hot_retention_hours'
   AND scope = 'platform';

COMMIT;
