-- Migration 576 DOWN: restore the historical TEXT type on request_logs_hot.

BEGIN;

ALTER TABLE public.request_logs_hot
    ALTER COLUMN customer_id TYPE text
    USING customer_id::text;

COMMIT;
