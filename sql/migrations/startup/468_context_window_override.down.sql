-- Migration 468 down: remove context_window_override columns

BEGIN;

ALTER TABLE public.models_canonical DROP COLUMN IF EXISTS context_window_override;
ALTER TABLE public.models_canonical DROP COLUMN IF EXISTS context_window_source;
ALTER TABLE public.models_canonical DROP COLUMN IF EXISTS context_window_updated_at;

COMMIT;
