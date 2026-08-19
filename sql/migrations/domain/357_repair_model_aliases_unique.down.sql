-- 357_repair_model_aliases_unique.down.sql
-- Rollback: drop the unique constraint added by the up migration.
-- The deleted duplicate rows are NOT restored (they were exact duplicates and are
-- recoverable only from a pre-migration backup, e.g. the models_canonical/
-- model_aliases/provider_catalog pg_dump taken before applying 352-356).

BEGIN;

ALTER TABLE model_aliases
    DROP CONSTRAINT IF EXISTS uq_model_aliases_canonical_raw;

COMMIT;
