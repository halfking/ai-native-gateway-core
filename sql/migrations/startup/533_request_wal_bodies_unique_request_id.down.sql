-- Migration 533 down is intentionally non-destructive.
--
-- request_wal_bodies.request_id is the conflict arbiter required by the
-- production request logger and was already part of the original 032 schema.
-- Dropping it on rollback would reintroduce the write failure and could not
-- distinguish a pre-existing constraint from one repaired by 533.
BEGIN;
-- Forward-compatible schema is preserved on rollback.
COMMIT;
