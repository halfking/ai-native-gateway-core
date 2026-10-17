-- Rollback for migration 870_usage_ledger_recon_cover_idx.sql.
-- Drops the index only; no data is touched.
--
-- ⚠ CONCURRENTLY note: this runs inside the channel's single-transaction chain,
-- so it cannot use DROP INDEX CONCURRENTLY. Dropping an index takes an
-- ACCESS EXCLUSIVE lock on the parent (and cascades to partitions), which
-- blocks writes for the duration of the (metadata-only) drop — normally
-- milliseconds. Do it in a low-traffic window anyway, for the same reason
-- the up-migration uses lock_timeout.

BEGIN;

SET LOCAL statement_timeout = '5min';
SET LOCAL lock_timeout = '2s';

DROP INDEX IF EXISTS public.idx_usage_ledger_recon_cover;

COMMIT;
