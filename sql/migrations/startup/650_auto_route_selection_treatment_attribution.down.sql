-- Migration 650 rollback: remove AUTO_MODEL V3 treatment attribution.
-- This intentionally discards the four nullable attribution fields.

BEGIN;

ALTER TABLE public.auto_route_selections
    DROP COLUMN IF EXISTS assignment_key_hash,
    DROP COLUMN IF EXISTS assignment_version,
    DROP COLUMN IF EXISTS treatment,
    DROP COLUMN IF EXISTS experiment_id;

DELETE FROM public.schema_migrations
WHERE version = '650';

COMMIT;
