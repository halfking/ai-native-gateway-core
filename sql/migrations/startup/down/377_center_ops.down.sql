-- 377_center_ops.down.sql
-- Rollback center ops tables

DROP TABLE IF EXISTS instance_heartbeats CASCADE;
DROP TABLE IF EXISTS gateway_instances CASCADE;
