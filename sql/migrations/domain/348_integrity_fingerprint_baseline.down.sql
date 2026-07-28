-- Rollback migration 348.
BEGIN;
DROP TABLE IF EXISTS integrity_fingerprint_baseline;
COMMIT;
