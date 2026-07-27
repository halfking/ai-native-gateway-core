-- Down migration for 461: drop the request_wal_hot(request_id) unique index.
-- Reverting restores the (request_id, created_at)-only constraint, under which
-- duplicate request_id rows are again permitted (and the early-vs-later
-- CreateInitial orphan can recur). Application code that relies on the
-- ON CONFLICT (request_id) path will fall back to no-op on conflict.

DROP INDEX IF EXISTS udx_request_wal_hot_request_id;
