-- Migration 575 DOWN: restore the previous request log view definition.
-- The canonical view object is re-applied by the deployment schema bundle.

BEGIN;
DROP VIEW IF EXISTS public.request_logs_with_current_month;
ALTER VIEW public.request_logs_with_current_month_without_customer_id
    RENAME TO request_logs_with_current_month;
COMMIT;
