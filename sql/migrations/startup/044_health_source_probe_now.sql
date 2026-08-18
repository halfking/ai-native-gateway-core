-- Migration 044: Add 'probe_now' to health_source constraint
--
-- Context: bg/credential_probe_v2.go sets health_source='probe_now' on the
-- fast path (no DB round-trip), but the database constraint only allowed
-- ['models', 'probe', 'mixed', 'none', 'fast_reprobe'] after migration 027.
--
-- This caused health updates with health_source='probe_now' to fail with:
--   ERROR: new row for relation "credentials" violates check constraint
--   "chk_credentials_health_source" (SQLSTATE 23514)
--
-- Impact: writeHealth failures left credentials (e.g. cred 22 / 25) stuck
-- in auth_failed / unreachable states, so model availability never recovered.
--
-- Fix: Add 'probe_now' to the allowed values list.

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
    'fast_reprobe'::text,
    'probe_now'::text
  ])
);
