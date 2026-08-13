-- Migration 478 down: remove model reasoning capability metadata.
--
-- This discards operator-configured reasoning capabilities. After rollback,
-- internal/reasoncap.Resolve falls back to the built-in model name patterns.

\set ON_ERROR_STOP on
BEGIN;

DROP INDEX IF EXISTS public.idx_models_canonical_reasoning_caps_supported;
DROP INDEX IF EXISTS public.idx_models_canonical_reasoning_caps_dialect;
ALTER TABLE public.models_canonical DROP COLUMN IF EXISTS reasoning_caps;

COMMIT;
