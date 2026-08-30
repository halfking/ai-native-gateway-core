-- Migration 621: support resolved provider error TTL cleanup.
-- The cleanup predicate uses updated_at, so a partial index avoids scanning
-- unresolved rows and keeps the hourly cleanup bounded as the table grows.

BEGIN;

CREATE INDEX IF NOT EXISTS idx_ped_resolved_updated_at
    ON public.provider_error_details (updated_at)
    WHERE resolved = true;

COMMENT ON INDEX idx_ped_resolved_updated_at IS
    'Supports cleanup of resolved provider errors older than the configured TTL';

COMMIT;
