-- Rollback for 446_volcengine_model_aliases.sql
-- This migration adds model aliases and can be safely rolled back by deleting them

-- Remove volcengine model aliases
DELETE FROM model_aliases WHERE model_canonical_name IN (
  'doubao-pro-32k',
  'doubao-lite-32k'
) AND provider_id = (SELECT id FROM providers WHERE name = 'volcengine' LIMIT 1);

