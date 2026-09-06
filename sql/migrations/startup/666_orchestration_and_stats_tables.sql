-- Migration 666: Orchestration Runtime Instances and LLM Statistics Tables
-- Purpose: Support external orchestration service and statistics collection
-- Created: 2026-09-06

-- ============================================
-- 1. Orchestration Runtime Instances Table
-- ============================================
-- This table is used by external orchestration services to register and track runtime instances
CREATE TABLE IF NOT EXISTS orchestration_runtime_instances (
  id BIGSERIAL PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  host_id TEXT,
  endpoint TEXT,
  status TEXT,
  capabilities JSONB,
  registration_revision INTEGER NOT NULL DEFAULT 0,
  lease_epoch BIGINT,
  credential_id TEXT,
  last_heartbeat_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT uq_orchestration_runtime_instance UNIQUE (tenant_id, runtime_id, instance_id)
);

CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_tenant
  ON orchestration_runtime_instances(tenant_id);

CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_runtime
  ON orchestration_runtime_instances(runtime_id);

CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_status
  ON orchestration_runtime_instances(status) WHERE status IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_heartbeat
  ON orchestration_runtime_instances(last_heartbeat_at DESC NULLS LAST);

COMMENT ON TABLE orchestration_runtime_instances IS
  'Runtime instance registrations from external orchestration services';

COMMENT ON COLUMN orchestration_runtime_instances.registration_revision IS
  'Auto-incremented on each UPDATE to track registration changes';

COMMENT ON COLUMN orchestration_runtime_instances.lease_epoch IS
  'Lease epoch for distributed coordination';

-- ============================================
-- 2. LLM Hourly Statistics Table
-- ============================================
-- Used by external statistics collection services to aggregate hourly metrics
CREATE TABLE IF NOT EXISTS llm_hourly_stats (
  hour TIMESTAMPTZ PRIMARY KEY,  -- Hour timestamp (YYYY-MM-DD HH:00:00+00)
  success_count INTEGER DEFAULT 0,
  failure_count INTEGER DEFAULT 0,
  total_count INTEGER DEFAULT 0,
  total_cost NUMERIC(12,6) DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT valid_counts CHECK (total_count >= 0 AND success_count >= 0 AND failure_count >= 0),
  CONSTRAINT valid_cost CHECK (total_cost >= 0)
);

CREATE INDEX IF NOT EXISTS idx_llm_hourly_stats_hour
  ON llm_hourly_stats(hour DESC);

COMMENT ON TABLE llm_hourly_stats IS
  'Hourly aggregated LLM request statistics from external collectors';

COMMENT ON COLUMN llm_hourly_stats.hour IS
  'Hour timestamp in UTC with timezone (must be exact hour: minutes=0, seconds=0)';

-- Trigger to update updated_at timestamp
CREATE OR REPLACE FUNCTION update_orchestration_runtime_instances_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_orchestration_runtime_instances_updated_at
  ON orchestration_runtime_instances;

CREATE TRIGGER trg_orchestration_runtime_instances_updated_at
  BEFORE UPDATE ON orchestration_runtime_instances
  FOR EACH ROW
  EXECUTE FUNCTION update_orchestration_runtime_instances_updated_at();

-- Trigger for llm_hourly_stats
CREATE OR REPLACE FUNCTION update_llm_hourly_stats_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_llm_hourly_stats_updated_at
  ON llm_hourly_stats;

CREATE TRIGGER trg_llm_hourly_stats_updated_at
  BEFORE UPDATE ON llm_hourly_stats
  FOR EACH ROW
  EXECUTE FUNCTION update_llm_hourly_stats_updated_at();
