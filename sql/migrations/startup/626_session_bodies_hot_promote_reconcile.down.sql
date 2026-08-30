-- Migration 626 is intentionally non-reversible.
--
-- Reverting its function body would restore a known data-integrity bug: a
-- destination conflict leaves the duplicate hot row eligible forever. Retain
-- the repaired idempotent promotion and security_invoker setting on downgrade.
BEGIN;
COMMIT;
