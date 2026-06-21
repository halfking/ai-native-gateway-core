-- Migration 027: Add 'fast_reprobe' to health_source constraint
--
-- Context: Commit d7aa4dc1 (2026-06-19) introduced fast reprobe functionality
-- in bg/credential_probe_v2.go which sets health_source='fast_reprobe', but
-- the database constraint only allowed ['models', 'probe', 'mixed', 'none'].
--
-- This caused all fast reprobe health updates to fail with:
--   ERROR: new row for relation "credentials" violates check constraint
--   "chk_credentials_health_source" (SQLSTATE 23514)
--
-- Impact: refresh-models, credential health checks, and model availability
-- probes all failed when fast reprobe was triggered after auth_failed or
-- unreachable states.
--
-- Fix: Add 'fast_reprobe' to the allowed values list.

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
