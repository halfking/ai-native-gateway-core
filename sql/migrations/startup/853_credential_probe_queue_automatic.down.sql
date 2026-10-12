-- Rollback for 853: drop the `automatic` column and its partial claim index.
-- Mirror of deploy-chain V360__credential_probe_queue_automatic.down.sql.
-- ⚠ Dropping the column makes bg/probe_queue.go enqueue/claim fail again
-- (42703); only run when rolling back the Go code as well.

DROP INDEX IF EXISTS idx_credential_probe_queue_automatic_claim;

ALTER TABLE public.credential_probe_queue
    DROP COLUMN IF EXISTS automatic;
