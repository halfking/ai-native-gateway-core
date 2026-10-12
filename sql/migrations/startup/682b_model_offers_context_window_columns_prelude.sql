-- Migration 682b: add the context_window bookkeeping columns that the
-- model_offers view rebuild in 682 selects.
--
-- Why this file exists (2026-10-10, host-first local deploy):
-- 682 recreates public.model_offers with
--     cmb.context_window_override, cmb.context_window_source,
--     cmb.context_window_updated_at
-- but the schema snapshot only creates context_window_override on
-- public.credential_model_bindings. On a host that has never booted the
-- gateway's Go ensure chain, the sequence leg runs first and 682 aborted with
--     ERROR: column cmb.context_window_source does not exist
-- which left the deploy permanently stuck: the migration ledger never
-- advanced past 682, so every later file in the sequence was blocked too.
--
-- The natural fix was 523_credential_model_context_window_override.sql, which
-- adds the same columns — but 523 also rebuilds the model_offers view in an
-- older shape, and against the current snapshot that fails with
--     ERROR: cannot change name of view column "raw_model_name" to
--            "canonical_raw_name"
-- before it ever reaches the column work. So this file does the minimum:
-- add exactly the two missing columns, touch nothing else.
--
-- Idempotent: every statement is guarded with IF NOT EXISTS, and the whole
-- body is wrapped in an existence check on the table so it is a silent no-op
-- on databases (e.g. minimal lite installs) that have no bindings table.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'credential_model_bindings'
  ) THEN
    RAISE NOTICE '682b: public.credential_model_bindings absent; nothing to add';
    RETURN;
  END IF;

  ALTER TABLE public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS context_window_source TEXT;
  ALTER TABLE public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS context_window_updated_at TIMESTAMPTZ;

  COMMENT ON COLUMN public.credential_model_bindings.context_window_source IS
    'Provenance of context_window_override: manual | provider | inferred';
  COMMENT ON COLUMN public.credential_model_bindings.context_window_updated_at IS
    'When context_window_override / context_window_source last changed';
END
$$;
