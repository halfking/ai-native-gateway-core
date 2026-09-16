-- Migration 087 rollback: Remove template health feedback columns

BEGIN;

DROP INDEX IF EXISTS public.idx_provider_templates_health;

ALTER TABLE public.provider_templates
    DROP COLUMN IF EXISTS consecutive_scan_failures,
    DROP COLUMN IF EXISTS last_scan_failure_at,
    DROP COLUMN IF EXISTS auto_disabled_at;

COMMIT;
