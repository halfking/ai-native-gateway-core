-- 352_add_grok_4_6.down.sql
-- Rollback grok-4.6 model addition

BEGIN;

-- Remove model aliases for grok-4.6
DELETE FROM model_aliases
WHERE canonical_id IN (
    SELECT id FROM models_canonical WHERE canonical_name = 'grok-4.6'
);

-- Remove grok-4.6 from models_canonical
DELETE FROM models_canonical
WHERE canonical_name = 'grok-4.6';

COMMIT;
