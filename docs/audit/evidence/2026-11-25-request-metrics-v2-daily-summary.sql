-- Run against a disposable restored 211 copy after 983d has caught up.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL jit = off;
SET LOCAL TIME ZONE 'Asia/Shanghai';

CREATE TEMP TABLE projection_input AS
SELECT occurred_at, tenant_id, request_id, session_id, status, total_tokens,
       cached_tokens, cache_input_tokens, cost_currency, cost_amount,
       credits_charged, latency_ms
FROM public.request_metrics_projection
WHERE traffic_class = 'business' AND occurred_at < DATE '2026-11-25';

CREATE TEMP VIEW projected_days AS
SELECT occurred_at::date AS day, * FROM projection_input;

CREATE TEMP VIEW daily_base AS
SELECT day, tenant_id, cost_currency, count(*) AS requests,
       count(*) FILTER (WHERE status='success') AS successes,
       count(*) FILTER (WHERE cached_tokens>0) AS cache_hits,
       count(cache_input_tokens) AS cache_covered,
       coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0) AS eligible_cache_reads,
       coalesce(sum(cache_input_tokens),0) AS cache_input,
       sum(total_tokens) AS tokens, sum(credits_charged) AS credits,
       sum(cost_amount) FILTER (WHERE cost_currency IS NOT NULL) AS original_amount
FROM projected_days GROUP BY day, tenant_id, cost_currency;

CREATE TEMP VIEW daily_session_base AS
SELECT DISTINCT day, tenant_id, session_id FROM projected_days WHERE session_id IS NOT NULL;

CREATE TEMP VIEW daily_latency_base AS
SELECT day, latency_ms, count(*) AS samples
FROM projected_days WHERE status='success' AND latency_ms>0
GROUP BY day, latency_ms;

CREATE TEMP TABLE daily_totals AS SELECT * FROM daily_base;
CREATE TEMP TABLE daily_sessions AS SELECT * FROM daily_session_base;
CREATE TEMP TABLE daily_latency AS SELECT * FROM daily_latency_base;

CREATE TEMP VIEW raw_window AS
SELECT jsonb_build_array(count(*), count(*) FILTER (WHERE status='success'),
       count(*) FILTER (WHERE cached_tokens>0), count(cache_input_tokens),
       coalesce(sum(cached_tokens) FILTER (WHERE cache_input_tokens IS NOT NULL),0),
       coalesce(sum(cache_input_tokens),0), sum(total_tokens), sum(credits_charged)) AS measures,
       count(DISTINCT (tenant_id, session_id)) FILTER (WHERE session_id IS NOT NULL) AS sessions,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
          FILTER (WHERE status='success' AND latency_ms>0) AS p95
FROM projected_days WHERE day >= DATE '2026-11-18' AND day < DATE '2026-11-25';

CREATE TEMP VIEW rolled_window AS
WITH histogram AS (
  SELECT latency_ms, sum(samples) AS samples FROM daily_latency
  WHERE day >= DATE '2026-11-18' AND day < DATE '2026-11-25' GROUP BY latency_ms
), rank_bounds AS (
  SELECT latency_ms, sum(samples) OVER (ORDER BY latency_ms) AS rank_hi,
         sum(samples) OVER (ORDER BY latency_ms)-samples AS rank_low FROM histogram
), rank_target AS (
  SELECT 1 + (sum(samples)-1)*0.95 AS rank FROM histogram
), rank_values AS (
  SELECT min(latency_ms) FILTER (WHERE floor(rank) > rank_low AND floor(rank) <= rank_hi) AS lo,
         min(latency_ms) FILTER (WHERE ceil(rank) > rank_low AND ceil(rank) <= rank_hi) AS hi,
         max(rank-floor(rank)) AS factor FROM rank_bounds CROSS JOIN rank_target
)
SELECT jsonb_build_array(sum(requests),sum(successes),sum(cache_hits),sum(cache_covered),
       sum(eligible_cache_reads),sum(cache_input),sum(tokens),sum(credits)) AS measures,
       (SELECT count(*) FROM (
         SELECT DISTINCT tenant_id,session_id FROM daily_sessions
         WHERE day >= DATE '2026-11-18' AND day < DATE '2026-11-25'
       ) membership) AS sessions,
       (SELECT lo+(hi-lo)*factor FROM rank_values) AS p95
FROM daily_totals WHERE day >= DATE '2026-11-18' AND day < DATE '2026-11-25';

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM raw_window r CROSS JOIN rolled_window a
    WHERE r.measures IS DISTINCT FROM a.measures OR r.sessions IS DISTINCT FROM a.sessions
       OR (r.p95 IS NULL) <> (a.p95 IS NULL) OR abs(r.p95-a.p95) > 0.000001
  ) THEN
    RAISE EXCEPTION 'daily summary changed the window totals, session cardinality, or latency percentile';
  END IF;
END $$;
SELECT 'initial_equivalence', (SELECT count(*) FROM projection_input),
       (SELECT count(*) FROM daily_totals), (SELECT count(*) FROM daily_sessions),
       (SELECT count(*) FROM daily_latency);

-- A late positive usage fact and its corrected version invalidate one day.
INSERT INTO projection_input
VALUES ('2026-11-23 12:00:00+08','fixture-tenant','late-fixture','fixture-session',
        'success',37,9,30,'CNY',0.456,5,432);
UPDATE projection_input SET total_tokens=41,cost_amount=0.567,latency_ms=765
WHERE tenant_id='fixture-tenant' AND request_id='late-fixture';
DELETE FROM daily_totals WHERE day=DATE '2026-11-23';
DELETE FROM daily_sessions WHERE day=DATE '2026-11-23';
DELETE FROM daily_latency WHERE day=DATE '2026-11-23';
INSERT INTO daily_totals SELECT * FROM daily_base WHERE day=DATE '2026-11-23';
INSERT INTO daily_sessions SELECT * FROM daily_session_base WHERE day=DATE '2026-11-23';
INSERT INTO daily_latency SELECT * FROM daily_latency_base WHERE day=DATE '2026-11-23';

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM raw_window r CROSS JOIN rolled_window a
    WHERE r.measures IS DISTINCT FROM a.measures OR r.sessions IS DISTINCT FROM a.sessions
       OR (r.p95 IS NULL) <> (a.p95 IS NULL) OR abs(r.p95-a.p95) > 0.000001
  ) OR NOT EXISTS (
    SELECT 1 FROM daily_totals WHERE tenant_id='fixture-tenant' AND day=DATE '2026-11-23'
      AND cost_currency='CNY' AND original_amount=0.567 AND tokens=41 AND requests=1
  ) THEN
    RAISE EXCEPTION 'late fact revision was not rebuilt with exact tokens, cost and percentile';
  END IF;
END $$;
SELECT 'late_revision_equivalence', true;

ANALYZE projection_input, daily_totals, daily_sessions, daily_latency;
EXPLAIN (ANALYZE, TIMING OFF, SUMMARY ON) SELECT * FROM raw_window;
EXPLAIN (ANALYZE, TIMING OFF, SUMMARY ON) SELECT * FROM rolled_window;
ROLLBACK;
