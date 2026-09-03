-- Migration 646: add treatment attribution to auto-route selections.
--
-- The selection writer records rollout metadata only for enrolled requests.
-- Values are nullable so unenrolled rows remain NULL rather than being
-- misclassified as control. No prompt, message, tenant, or request payload is
-- stored by this migration.

BEGIN;

ALTER TABLE public.auto_route_selections
    ADD COLUMN IF NOT EXISTS experiment_id TEXT,
    ADD COLUMN IF NOT EXISTS treatment TEXT,
    ADD COLUMN IF NOT EXISTS assignment_version TEXT,
    ADD COLUMN IF NOT EXISTS assignment_key_hash TEXT;

INSERT INTO public.schema_migrations (version, description)
VALUES (
    '646',
    'auto_route_selections: treatment attribution fields for AUTO_MODEL V3'
)
ON CONFLICT (version) DO NOTHING;

COMMIT;
