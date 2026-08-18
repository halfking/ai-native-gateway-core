-- Rollback for migration 044_health_source_probe_now.sql.
-- Restores the pre-044 constraint state (post-migration 027): allows
-- ['models', 'probe', 'mixed', 'none', 'fast_reprobe'], drops 'probe_now'.

ALTER TABLE credentials 
DROP CONSTRAINT IF EXISTS chk_credentials_health_source;

ALTER TABLE credentials 
ADD CONSTRAINT chk_credentials_health_source 
CHECK (
  health_source IS NULL 
  OR health_source = ANY (ARRAY[
    'models'::text, 
    'probe'::text, 
    'mixed'::text, 
    'none'::text, 
    'fast_reprobe'::text
  ])
);
