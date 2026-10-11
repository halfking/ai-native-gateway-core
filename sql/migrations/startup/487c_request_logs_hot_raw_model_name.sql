-- Migration 487c: align request_logs_hot with the columns that later view
-- rebuilds read from BOTH the parent and the hot heap.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- The request_logs_with_current_month view is rebuilt several times by the
-- sequence (680, 696, 700). Each rebuild defines it as
--
--     SELECT v.*, source.request_class, source.due_at,
--            source.system_fingerprint, source.raw_model_name
--     FROM request_logs_with_current_month_without_request_class_due_at v
--     LEFT JOIN LATERAL (
--         SELECT h.request_class, h.due_at, h.system_fingerprint, h.raw_model_name
--         FROM request_logs_hot h ...
--         UNION ALL
--         SELECT p.request_class, p.due_at, p.system_fingerprint, p.raw_model_name
--         FROM request_logs p ...
--     ) source ON true;
--
-- so every one of those columns must exist on BOTH relations. The migrations
-- that introduce them only touch the partitioned parent:
--   485 → request_logs.raw_model_name
--   487 → request_logs.system_fingerprint
--   610 → request_logs{,_hot}.request_class / due_at
-- and request_logs_hot is an independent heap with its own column list, so
-- adding a column to the parent does NOT add it to the hot table. Result on a
-- fresh database: 696 aborted with 42703 on h.system_fingerprint and, once
-- that was fixed, 700 aborted with 42703 on h.raw_model_name — the sequence
-- never advanced past either.
--
-- 487b covers system_fingerprint only. This file covers the remaining pair so
-- the whole hot-heap alignment is in one place.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS, guarded by an existence check on the
-- table so it is a no-op where the hot table does not exist.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'request_logs_hot'
  ) THEN
    RAISE NOTICE '487c: public.request_logs_hot absent; nothing to add';
    RETURN;
  END IF;

  ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS raw_model_name TEXT;

  COMMENT ON COLUMN public.request_logs_hot.raw_model_name IS
    'Provider-reported model name before canonicalization; mirrors request_logs.raw_model_name (485) for the hot heap';
END
$$;
