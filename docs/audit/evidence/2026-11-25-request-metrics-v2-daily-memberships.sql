-- Run against the isolated 90-day PostgreSQL 17 scale fixture.
-- Session codes and daily masks are temporary and all fixture edits roll back.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL TIME ZONE 'Asia/Shanghai';
SET LOCAL jit = off;

CREATE TEMP TABLE membership_input AS
SELECT occurred_at::date AS day, tenant_id, session_id
FROM public.request_metrics_projection
WHERE source_fact_id IS NOT NULL AND traffic_class='business'
  AND occurred_at >= timestamptz '2026-08-27 00:00:00+08'
  AND occurred_at < timestamptz '2026-11-25 00:00:00+08' AND session_id IS NOT NULL;
CREATE TEMP TABLE session_codes (
  tenant_id text NOT NULL, session_id text NOT NULL,
  code integer GENERATED ALWAYS AS IDENTITY,
  PRIMARY KEY (tenant_id,session_id)
);
INSERT INTO session_codes (tenant_id,session_id)
SELECT DISTINCT tenant_id,session_id FROM membership_input;
CREATE TEMP VIEW mask_components AS
SELECT day, code/64 AS word, bit_or(1::bigint << (code%64)) AS mask
FROM membership_input JOIN session_codes USING (tenant_id,session_id)
GROUP BY day, code/64;
CREATE TEMP TABLE day_masks AS SELECT * FROM mask_components;
CREATE TEMP VIEW direct_window AS
SELECT count(DISTINCT (tenant_id,session_id)) AS active_sessions
FROM membership_input WHERE day >= date '2026-08-27' AND day < date '2026-11-25';
CREATE TEMP VIEW combined_window AS
SELECT coalesce(sum(bit_count(mask::bit(64))),0)::bigint AS active_sessions
FROM (
  SELECT bit_or(mask) AS mask FROM day_masks
  WHERE day >= date '2026-08-27' AND day < date '2026-11-25' GROUP BY word
) words;

DO $$
BEGIN
  IF (SELECT active_sessions FROM direct_window) <> (SELECT active_sessions FROM combined_window) THEN
    RAISE EXCEPTION 'session membership count differs';
  END IF;
END $$;
SELECT 'initial_membership', (SELECT active_sessions FROM combined_window),
       (SELECT count(*) FROM membership_input), (SELECT count(*) FROM day_masks);

-- Late evidence reuses one session ID across two tenants and two days.
INSERT INTO membership_input VALUES
 ('2026-08-28','mask-fixture-a','id'), ('2026-08-29','mask-fixture-a','id'),
 ('2026-08-28','mask-fixture-b','id');
INSERT INTO session_codes (tenant_id,session_id)
SELECT DISTINCT tenant_id,session_id FROM membership_input WHERE tenant_id IN ('mask-fixture-a','mask-fixture-b');
DELETE FROM day_masks WHERE day IN ('2026-08-28','2026-08-29');
INSERT INTO day_masks SELECT * FROM mask_components WHERE day IN ('2026-08-28','2026-08-29');
DO $$
BEGIN
  IF (SELECT active_sessions FROM direct_window) <> (SELECT active_sessions FROM combined_window) THEN
    RAISE EXCEPTION 'late evidence membership count differs';
  END IF;
END $$;
SELECT 'late_membership',(SELECT active_sessions FROM combined_window);

-- A repair moves the last two occurrences from one tenant to an existing
-- session in another tenant. The old membership must disappear on rebuild.
UPDATE membership_input SET tenant_id='mask-fixture-b'
WHERE tenant_id='mask-fixture-a' AND session_id='id';
DELETE FROM day_masks WHERE day IN ('2026-08-28','2026-08-29');
INSERT INTO day_masks SELECT * FROM mask_components WHERE day IN ('2026-08-28','2026-08-29');
DO $$
BEGIN
  IF (SELECT active_sessions FROM direct_window) <> (SELECT active_sessions FROM combined_window) THEN
    RAISE EXCEPTION 'repaired evidence membership count differs';
  END IF;
END $$;
SELECT 'repaired_membership',(SELECT active_sessions FROM combined_window);

ANALYZE membership_input,day_masks;
EXPLAIN (ANALYZE,TIMING OFF) SELECT * FROM direct_window;
EXPLAIN (ANALYZE,TIMING OFF) SELECT * FROM combined_window;
ROLLBACK;
