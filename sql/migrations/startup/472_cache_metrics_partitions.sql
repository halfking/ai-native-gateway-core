-- Migration 472: cache_metrics default + recent partitions — fix D2 partition gap
--
-- Background:
--   Migration 470 (e1a002a6c) created cache_metrics as a RANGE-partitioned
--   table by partition_date but did NOT create any partition (default or
--   daily). Every INSERT fails with "no partition of relation cache_metrics
--   found for row" until partitions exist. The DBRecorder.Record path
--   (domains/cachemetrics/recorder.go) silently swallows the error and
--   the cache layer emits no telemetry.
--
--   This migration closes the gap by creating:
--     1. cache_metrics_default — DEFAULT partition (writes go here until
--        the daily partition exists; aligned with rule 33 §2 INSERT 铁律)
--     2. cache_metrics_2026_08 + cache_metrics_2026_09 — monthly ranges
--        covering deploy time + next month (rule 33 §6.1 预创建下月分区)
--
--   Future partition lifecycle (daily drop) lives in
--   scripts/partitions/migrate-default-to-monthly.sh per rule 33.
--
-- Idempotent: IF NOT EXISTS guards; safe to re-run.
--
-- Rollback: DROP TABLE cache_metrics_default CASCADE (does not drop the
--           already-applied migration 470 schema/constraints).

BEGIN;

DO $$
BEGIN
  -- 1. Default partition — catches all writes until daily/monthly partitions exist
  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits
    WHERE inhparent = 'public.cache_metrics'::regclass
      AND inhrelid = 'public.cache_metrics_default'::regclass
  ) THEN
    CREATE TABLE public.cache_metrics_default
      PARTITION OF public.cache_metrics DEFAULT;
    COMMENT ON TABLE public.cache_metrics_default IS 'D2: default partition for cache_metrics (rule 33 default pattern)';
  END IF;

  -- 2. Monthly partitions — 2026_08 (deploy month) + 2026_09 (next month)
  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits
    WHERE inhparent = 'public.cache_metrics'::regclass
      AND inhrelid = 'public.cache_metrics_2026_08'::regclass
  ) THEN
    CREATE TABLE public.cache_metrics_2026_08
      PARTITION OF public.cache_metrics
      FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
    COMMENT ON TABLE public.cache_metrics_2026_08 IS 'D2: 2026-08 monthly partition for cache_metrics';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_inherits
    WHERE inhparent = 'public.cache_metrics'::regclass
      AND inhrelid = 'public.cache_metrics_2026_09'::regclass
  ) THEN
    CREATE TABLE public.cache_metrics_2026_09
      PARTITION OF public.cache_metrics
      FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
    COMMENT ON TABLE public.cache_metrics_2026_09 IS 'D2: 2026-09 monthly partition for cache_metrics';
  END IF;

  -- 3. Ensure indexes propagate to new partitions (PG auto-propagates parent
  --    indexes on partition creation, but explicit recreation here makes
  --    the migration re-runnable after manual partition drops)
  CREATE INDEX IF NOT EXISTS idx_cache_metrics_default_tenant_ts
    ON public.cache_metrics_default (tenant_id, cache_layer, recorded_at DESC);
  CREATE INDEX IF NOT EXISTS idx_cache_metrics_2026_08_event_type
    ON public.cache_metrics_2026_08 (event_type, recorded_at DESC);
  CREATE INDEX IF NOT EXISTS idx_cache_metrics_2026_09_event_type
    ON public.cache_metrics_2026_09 (event_type, recorded_at DESC);
END $$;

COMMIT;