-- Migration 601 down: no-op.
--
-- Body rows intentionally do not duplicate request metadata. Re-adding the
-- dropped column would recreate the schema mismatch that caused failed writes.

\set ON_ERROR_STOP on
