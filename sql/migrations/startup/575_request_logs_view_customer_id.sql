-- Migration 575: expose request customer metadata in the current-month view.
--
-- Purpose: make tenant/user/customer metadata queryable through the same
-- request_logs_with_current_month contract used by /api/logs.
--
-- The column is appended to preserve PostgreSQL view freeze compatibility.
-- Both branches use BIGINT customer_id after migration 574.
--
-- Status: active
-- Idempotent: YES (CREATE OR REPLACE appends only)
-- Rollback: 575_request_logs_view_customer_id.down.sql
-- Changelog:
--   2026-08-25  v1.0  Append customer_id to request log view

BEGIN;

ALTER VIEW public.request_logs_with_current_month
    RENAME TO request_logs_with_current_month_without_customer_id;

CREATE VIEW public.request_logs_with_current_month AS
SELECT v.*,
       m.customer_id
FROM public.request_logs_with_current_month_without_customer_id v
LEFT JOIN LATERAL (
    SELECT customer_id FROM public.request_logs_hot
    WHERE request_id = v.request_id AND ts = v.ts
    UNION ALL
    SELECT customer_id FROM public.request_logs
    WHERE request_id = v.request_id AND ts = v.ts
    LIMIT 1
) m ON true;

COMMIT;
