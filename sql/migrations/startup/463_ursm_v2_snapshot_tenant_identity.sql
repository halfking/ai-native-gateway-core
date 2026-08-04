-- Migration: 463_ursm_v2_snapshot_tenant_identity
-- Purpose: preserve tenant-scoped URSM v2 runtime state in minute snapshots.
BEGIN;

UPDATE ursm_node_snapshot_min
SET tenant_id = ''
WHERE tenant_id IS NULL;

ALTER TABLE ursm_node_snapshot_min
  ALTER COLUMN tenant_id SET DEFAULT '',
  ALTER COLUMN tenant_id SET NOT NULL;

ALTER TABLE ursm_node_snapshot_min
  DROP CONSTRAINT IF EXISTS ursm_node_snapshot_min_pkey;

ALTER TABLE ursm_node_snapshot_min
  ADD PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

COMMIT;
