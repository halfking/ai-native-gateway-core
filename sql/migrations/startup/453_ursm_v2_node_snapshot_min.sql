-- Migration: 453_ursm_v2_node_snapshot_min
-- Purpose: minute-granularity snapshot table for URSM v2 Redis state.
--   domains/ursm/v2/persist.Writer.Collect → Flush writes one row per
--   (snapshot_ts, credential_id, raw_model_name) so the v2 runtime state
--   (availability, SR windows, latency, score) is recoverable from PG after
--   a Redis flush / restart. Previously misfiled as domain/450 which collided
--   with startup/450 (request_stage_events_tenant) and was never applied.
BEGIN;

CREATE TABLE IF NOT EXISTS ursm_node_snapshot_min (
  snapshot_ts        timestamptz NOT NULL,
  recovery_epoch     bigint      NOT NULL,
  provider_id        int         NOT NULL,
  credential_id      int         NOT NULL,
  raw_model_name     text        NOT NULL,
  canonical_name     text,
  tenant_id          text,
  available          boolean     NOT NULL,
  health_status      text,
  fail_streak        int,
  cool_until         timestamptz,
  sr_1m              real,
  sr_5m              real,
  sr_30m             real,
  samples_1m         int,
  samples_5m         int,
  samples_30m        int,
  lat_p50_ms         int,
  lat_p95_ms         int,
  score              real,
  price_in_per_1m    numeric,
  price_out_per_1m   numeric,
  billing_mode       text,
  trust_level        real,
  baseurl_latency_ms int,
  conc_used          int,
  conc_limit         int,
  fp_used            int,
  fp_limit           int,
  source_priority    int,
  generation         bigint,
  payload            jsonb,
  PRIMARY KEY (snapshot_ts, credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS ursm_node_snapshot_min_ts_idx
  ON ursm_node_snapshot_min(snapshot_ts);

COMMIT;
