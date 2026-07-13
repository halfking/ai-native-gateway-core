-- 390_routing_audit_log.down.sql
-- Reverses the Phase-2 audit log and diagnostic run tables. Does
-- NOT touch route_incidents or any pre-existing table.

BEGIN;

DROP TRIGGER IF EXISTS diagnostic_runs_touch ON diagnostic_runs;
DROP TABLE IF EXISTS diagnostic_runs;
DROP TABLE IF EXISTS routing_audit_log;

COMMIT;
