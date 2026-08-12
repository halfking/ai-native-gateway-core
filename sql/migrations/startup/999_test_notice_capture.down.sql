-- Migration 999 (TEST, down): remove the test marker if it persists as schema data.
-- This test migration only emits a NOTICE, so this down is a no-op.
BEGIN;
COMMIT;
