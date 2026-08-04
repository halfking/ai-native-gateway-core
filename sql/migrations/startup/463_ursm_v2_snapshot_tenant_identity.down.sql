-- Rollback: 463_ursm_v2_snapshot_tenant_identity
-- Note: historical tenant identity cannot be safely collapsed. This rollback
-- restores the old key shape only after confirming no same-minute tenant rows
-- would collide.
BEGIN;

ALTER TABLE ursm_node_snapshot_min
  DROP CONSTRAINT IF EXISTS ursm_node_snapshot_min_pkey;

ALTER TABLE ursm_node_snapshot_min
  ALTER COLUMN tenant_id DROP DEFAULT,
  ALTER COLUMN tenant_id DROP NOT NULL;

ALTER TABLE ursm_node_snapshot_min
  ADD PRIMARY KEY (snapshot_ts, credential_id, raw_model_name);

COMMIT;
