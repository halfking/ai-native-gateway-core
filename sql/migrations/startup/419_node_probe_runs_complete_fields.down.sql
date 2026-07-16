-- Rollback for: 419_node_probe_runs_complete_fields
-- Purpose: 回滚 node_probe_runs 表的字段扩展

-- 删除索引
DROP INDEX IF EXISTS idx_node_probe_runs_api_model;
DROP INDEX IF EXISTS idx_node_probe_runs_provider;

-- 删除新增字段
ALTER TABLE node_probe_runs 
  DROP COLUMN IF EXISTS api_model,
  DROP COLUMN IF EXISTS outbound_model,
  DROP COLUMN IF EXISTS provider_id,
  DROP COLUMN IF EXISTS request_url,
  DROP COLUMN IF EXISTS request_headers,
  DROP COLUMN IF EXISTS request_body,
  DROP COLUMN IF EXISTS response_body,
  DROP COLUMN IF EXISTS timeout_at_ms,
  DROP COLUMN IF EXISTS via_proxy;
