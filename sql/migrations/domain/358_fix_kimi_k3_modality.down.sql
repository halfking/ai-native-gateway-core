-- 358_fix_kimi_k3_modality.down.sql
-- Revert kimi-k3 modality to the value written by migration 354 ('vision').
-- The removed duplicate rows are NOT restored (they were exact duplicates and are
-- recoverable only from a pre-migration backup, e.g. the models_canonical/
-- model_aliases/provider_catalog pg_dump taken before applying 352-356).

BEGIN;

UPDATE models_canonical
SET modality = 'vision', updated_at = NOW()
WHERE canonical_name = 'kimi-k3';

COMMIT;
