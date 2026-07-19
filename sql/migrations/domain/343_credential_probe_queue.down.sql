-- Migration 343 down: remove the persistent self-check queue.
BEGIN;
DROP TABLE IF EXISTS credential_probe_queue;
COMMIT;
