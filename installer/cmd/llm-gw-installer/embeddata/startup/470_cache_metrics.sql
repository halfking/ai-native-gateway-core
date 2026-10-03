-- Migration 470: cache_metrics — unified cache observability (D2)
--
-- docs/omni-ref3 D2: create unified cache_metrics table for all cache layers
-- (semantic/prefix/delta/kv/session-state). Enables answering "overall cache
-- hit rate" and "tokens saved by cache" without scattered counters.
--
-- Schema:
--   tenant_id       — tenant partition key
--   cache_layer     — 'semantic', 'prefix', 'delta', 'kv', 'session_state'
--   event_type      — 'hit', 'miss'
--   tokens_saved    — tokens saved on hit (0 on miss)
--   session_id      — optional session context
--   request_id      — optional request context
--   recorded_at     — event timestamp
--
-- Retention: partition by day, drop partitions older than 30 days (configurable).
--
-- Idempotent: IF NOT EXISTS guards prevent duplicate table errors on re-run.

BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_tables
    WHERE schemaname = 'public' AND tablename = 'cache_metrics'
  ) THEN
    CREATE TABLE public.cache_metrics (
      id BIGSERIAL NOT NULL,
      tenant_id TEXT NOT NULL,
      cache_layer TEXT NOT NULL,
      event_type TEXT NOT NULL,
      tokens_saved INTEGER DEFAULT 0,
      session_id TEXT,
      request_id TEXT,
      recorded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW() NOT NULL,
      partition_date DATE DEFAULT CURRENT_DATE NOT NULL
    ) PARTITION BY RANGE (partition_date);

    COMMENT ON TABLE public.cache_metrics IS 'D2: unified cache hit/miss telemetry for all cache layers';
    COMMENT ON COLUMN public.cache_metrics.cache_layer IS 'semantic, prefix, delta, kv, session_state';
    COMMENT ON COLUMN public.cache_metrics.event_type IS 'hit, miss';
    COMMENT ON COLUMN public.cache_metrics.tokens_saved IS 'Tokens saved on cache hit (0 on miss)';

    -- Create index for aggregation queries
    CREATE INDEX idx_cache_metrics_tenant_layer_ts
      ON public.cache_metrics (tenant_id, cache_layer, recorded_at DESC);

    -- Create index for event_type filtering
    CREATE INDEX idx_cache_metrics_event_type
      ON public.cache_metrics (event_type, recorded_at DESC);

    -- Constraint: event_type must be 'hit' or 'miss'
    ALTER TABLE public.cache_metrics
      ADD CONSTRAINT cache_metrics_event_type_check
      CHECK (event_type IN ('hit', 'miss'));

    -- Constraint: cache_layer must be one of known layers
    ALTER TABLE public.cache_metrics
      ADD CONSTRAINT cache_metrics_cache_layer_check
      CHECK (cache_layer IN ('semantic', 'prefix', 'delta', 'kv', 'session_state'));
  END IF;
END $$;

COMMIT;
