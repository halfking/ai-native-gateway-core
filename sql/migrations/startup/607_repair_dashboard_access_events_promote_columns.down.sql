-- Down for 607: remove the repaired dashboard_access_events hot-promote
-- function. The lifecycle setting and the feature-level down path are
-- owned by migration 579's down script; this only reverts the function
-- replacement. Rolling 607 back on a database where 579 also ran leaves no
-- promote function at all (579's original body was broken and is not
-- restored).

\set ON_ERROR_STOP on

BEGIN;

DROP FUNCTION IF EXISTS public.promote_dashboard_access_events_hot_to_partition(interval, integer);

COMMIT;
