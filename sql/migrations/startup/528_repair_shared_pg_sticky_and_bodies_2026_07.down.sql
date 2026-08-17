-- Down migration 528: intentionally non-destructive.
--
-- Both repaired objects remain compatible with pre-528 binaries:
--   - the sticky unique constraint is required by the existing ON CONFLICT path;
--   - the historical partition may contain promoted audit bodies immediately
--     after deployment.
--
-- Removing either object cannot be made data-safe under concurrent traffic, and
-- the up migration may have reused an object created outside migration 528.
-- Binary rollback therefore keeps the forward-compatible schema in place.

BEGIN;

DO $$
BEGIN
    RAISE NOTICE '528 down is a no-op: preserving sticky conflict arbiter and historical audit partition';
END $$;

COMMIT;
