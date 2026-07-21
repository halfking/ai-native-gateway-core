-- Rollback: 453_ursm_v2_node_snapshot_min
BEGIN;
DROP TABLE IF EXISTS ursm_node_snapshot_min;
COMMIT;
