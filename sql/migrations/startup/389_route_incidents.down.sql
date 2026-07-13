-- 389_route_incidents.down.sql
-- Reverses the Phase-1 read-only incident tables. Does NOT touch
-- request_logs or any other in-use table.

BEGIN;

DROP TRIGGER IF EXISTS route_incidents_touch ON route_incidents;
DROP FUNCTION IF EXISTS touch_route_incidents_updated_at();

DROP TABLE IF EXISTS route_incident_events;
DROP TABLE IF EXISTS route_incidents;

COMMIT;
