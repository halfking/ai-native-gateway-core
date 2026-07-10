-- Migration 337 (down): 撤销 routing_health_checks 表
BEGIN;
DROP TABLE IF EXISTS routing_health_checks;
COMMIT;
