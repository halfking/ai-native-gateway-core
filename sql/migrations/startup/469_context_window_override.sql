-- Migration 469: context_window_override — manual calibration for A4
--
-- docs/omni-ref3 A4 Phase 1: add override column for manual context window
-- calibration. When providers mis-declare their context window (虚标), ops
-- can set an override value that takes precedence over the catalog default.
--
-- Columns:
--   context_window_override  — manually calibrated value (nullable, takes precedence)
--   context_window_source    — provenance: 'catalog', 'discovery', 'manual', 'probe'
--   context_window_updated_at — last update timestamp for audit
--
-- Future (A4 Phase 2): discovery service will populate context_window_override
-- from provider /v1/models API and set source='discovery'. For now, only
-- manual updates via admin API.
--
-- Idempotent: IF NOT EXISTS guards prevent duplicate column errors on re-run.

BEGIN;

DO $$
BEGIN
  -- Add override column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'models_canonical'
      AND column_name = 'context_window_override'
  ) THEN
    ALTER TABLE public.models_canonical ADD COLUMN context_window_override integer;
    COMMENT ON COLUMN public.models_canonical.context_window_override IS 'A4: manual override for mis-declared context windows. Takes precedence over context_window.';
  END IF;

  -- Add source column
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'models_canonical'
      AND column_name = 'context_window_source'
  ) THEN
    ALTER TABLE public.models_canonical ADD COLUMN context_window_source text DEFAULT 'catalog';
    COMMENT ON COLUMN public.models_canonical.context_window_source IS 'A4: provenance of effective context_window (catalog/discovery/manual/probe).';
  END IF;

  -- Add updated_at timestamp
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'models_canonical'
      AND column_name = 'context_window_updated_at'
  ) THEN
    ALTER TABLE public.models_canonical ADD COLUMN context_window_updated_at timestamp with time zone;
    COMMENT ON COLUMN public.models_canonical.context_window_updated_at IS 'A4: last manual/discovery update to context_window_override.';
  END IF;
END $$;

COMMIT;
