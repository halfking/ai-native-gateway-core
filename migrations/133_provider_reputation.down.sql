-- Rollback Migration 133: Provider Reputation System

-- 删除触发器和函数
DROP TRIGGER IF EXISTS trg_provider_incidents_duration ON provider_incidents;
DROP TRIGGER IF EXISTS trg_provider_incidents_updated_at ON provider_incidents;
DROP TRIGGER IF EXISTS trg_provider_rep_ts_updated_at ON provider_reputation_timeseries;
DROP FUNCTION IF EXISTS calculate_incident_duration();
DROP FUNCTION IF EXISTS update_provider_reputation_updated_at();

-- 删除表
DROP TABLE IF EXISTS provider_incidents;
DROP TABLE IF EXISTS provider_reputation_timeseries;
