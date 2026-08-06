BEGIN;

-- Migration 466 (down): Restore strict chk_compression_parent_single
--
-- Reverts 466_relax_compression_parent_check.sql. Re-adds the strict CHECK
-- (parent_request_id requires compression_reason).
--
-- IMPORTANT (data cleanup): once the relaxed constraint was in effect, loopback
-- correlation rows may exist with parent_request_id + origin_actor but no
-- compression_reason. Re-adding the strict CHECK would fail on those rows, so we
-- first null out parent_request_id on any row that is a correlation row
-- (parent_request_id NOT NULL AND compression_reason IS NULL AND origin_actor IS
-- NOT NULL). origin_actor is kept for audit. If the auto-title/auto-summary
-- loopback code (which sets parent_request_id from a header) is still deployed,
-- those rows will fail again with 23514 — roll back the code too.
--
-- Idempotency: existence guards before each DROP; re-running is safe.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint
             WHERE conname = 'chk_compression_parent_single'
               AND conrelid = 'public.request_logs'::regclass) THEN
    ALTER TABLE public.request_logs DROP CONSTRAINT chk_compression_parent_single;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_constraint
             WHERE conname = 'chk_compression_parent_single'
               AND conrelid = 'public.request_logs_hot'::regclass) THEN
    ALTER TABLE public.request_logs_hot DROP CONSTRAINT chk_compression_parent_single;
  END IF;

  -- Break loopback correlation links so the strict CHECK can be re-added.
  UPDATE public.request_logs_hot
     SET parent_request_id = NULL
   WHERE parent_request_id IS NOT NULL
     AND compression_reason IS NULL
     AND origin_actor IS NOT NULL;

  UPDATE public.request_logs
     SET parent_request_id = NULL
   WHERE parent_request_id IS NOT NULL
     AND compression_reason IS NULL
     AND origin_actor IS NOT NULL;

  ALTER TABLE public.request_logs
    ADD CONSTRAINT chk_compression_parent_single
    CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL);

  ALTER TABLE public.request_logs_hot
    ADD CONSTRAINT chk_compression_parent_single
    CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL);
END $$;

COMMIT;
