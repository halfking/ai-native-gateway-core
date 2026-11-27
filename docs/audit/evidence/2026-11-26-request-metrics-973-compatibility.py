"""Emit the reversible PostgreSQL proof for reusing the released 973 projection."""

import argparse
import subprocess

RELEASED = "6f0995251933e72fdbae55fb11f9b9ff8523906a"
CANDIDATE = "e833b9653a07861ad7077a4f6b086f3247e9700b"


def git_text(revision, path):
    return subprocess.check_output(["git", "show", f"{revision}:{path}"], text=True)


def sql(name):
    return git_text(CANDIDATE, f"sql/migrations/startup/{name}")


def body(text):
    return text.split("BEGIN;", 1)[1].split("COMMIT;", 1)[0].strip()


def statement(text, name):
    return text.split(f"const {name} = `", 1)[1].split("`", 1)[0].strip()


cli = argparse.ArgumentParser(description=__doc__)
cli.add_argument("--replay-release", action="store_true", help="Warm the installed 973 fixture with the released batch")
cli.add_argument("--explain", action="store_true", help="Print the repair execution plan")
cli.add_argument("--with-jit", action="store_true", help="Use the cluster JIT default for an A/B comparison")
options = cli.parse_args()

old = git_text(RELEASED, "internal/metricsstore/processor.go")
if options.replay_release:
    batch = statement(old, "projectBatchSQL")
    lock = " ".join(statement(old, "lockProjectionSQL").split())
    print(r"""\set ON_ERROR_STOP on
DO $$
BEGIN
    IF current_database() <> 'proof_baseline' OR
        NOT EXISTS (SELECT FROM public.schema_migrations WHERE version='973')
    THEN RAISE EXCEPTION 'expected the installed 973 proof fixture'; END IF;
END $$;""")
    print(f"PREPARE released_batch(integer) AS {batch};")
    print(f"SELECT 'BEGIN; {lock}; EXECUTE released_batch(500); COMMIT;' FROM generate_series(1,140)")
    print(r"\gexec")
    print("SELECT count(*) FILTER (WHERE source_fact_id IS NOT NULL), "
          "count(*) FILTER (WHERE source_fact_id IS NULL) FROM public.request_metrics;")
    print("SELECT backfill_complete FROM public.request_metrics_projection_state;")
    raise SystemExit(0)

new = git_text(CANDIDATE, "internal/metricsstore/projection.go")
released_batch = statement(old, "projectBatchSQL")
projection = body(sql("983d_request_metrics_projection.sql"))

# Preserve 973's fact queue and insert trigger. Add separate refresh events
# so that the new slot sees the key even if 973 acknowledges the insert first.
queue_start = projection.index("CREATE TABLE IF NOT EXISTS public.request_metrics_pending")
queue_end = projection.index("CREATE TABLE IF NOT EXISTS public.request_metrics_projection_state")
queue = projection[queue_start:queue_end].replace("request_metrics_pending", "request_metrics_refresh_pending")

events_start = projection.index("CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_fact()")
events_end = projection.index("INSERT INTO public.schema_migrations", events_start)
events = projection[events_start:events_end]
events = events.replace("request_metrics_pending", "request_metrics_refresh_pending")
events = events.replace("enqueue_request_metrics_fact", "enqueue_request_metrics_refresh_fact")

# 973 owns the physical relation. The view aliases its original fact request ID.
daily = body(sql("983e_request_metrics_daily_summary.sql"))
daily = daily.split("INSERT INTO public.schema_migrations", 1)[0]
daily = daily.replace("ON public.request_metrics_projection", "ON public.request_metrics")
index = body(sql("983h_request_metrics_daily_tenant_history.sql"))
index = "CREATE INDEX IF NOT EXISTS" + index.split("CREATE INDEX IF NOT EXISTS", 1)[1]
index = index.split("CREATE OR REPLACE FUNCTION", 1)[0]
index = index.replace("ON public.request_metrics_projection", "ON public.request_metrics")

adjusted = statement(new, "projectBatchSQL")
adjusted = adjusted.replace("public.request_metrics_pending", "public.request_metrics_refresh_pending")
adjusted = adjusted.replace("), keys AS MATERIALIZED", """
), released_pending AS MATERIALIZED (
    SELECT fact_id, occurred_at FROM public.request_metrics_pending
    ORDER BY enqueued_at, fact_id, occurred_at LIMIT $1 FOR UPDATE
), keys AS MATERIALIZED""", 1)
adjusted = adjusted.replace("SELECT tenant_id, request_id, occurred_at FROM history", """SELECT tenant_id, request_id, occurred_at FROM history
    UNION
    SELECT f.tenant_id, f.request_id, f.occurred_at FROM released_pending p
    JOIN public.usage_facts f USING (fact_id, occurred_at)""", 1)

# Preserve the released inference for old terminal inbox amounts and its
# reserved physical identity while using the candidate's repair semantics.
normalized = released_batch.split("), normalized AS (", 1)[1].split("), published AS", 1)[0]
adjusted = adjusted.replace("), published AS (", "), normalized AS (" + normalized + "), published AS (", 1)
adjusted = adjusted.replace("INSERT INTO public.request_metrics_projection AS rm (", "INSERT INTO public.request_metrics AS rm (")
adjusted = adjusted.replace(
    "request_id, tenant_id, occurred_at, occurred_date, billing_date,",
    "request_id, fact_request_id, tenant_id, occurred_at, occurred_date, billing_date,",
    1,
)
adjusted = adjusted.replace(
    "SELECT f.request_id,", "SELECT chr(1)||jsonb_build_array(f.tenant_id,f.request_id)::text, f.request_id,", 1
)
adjusted = adjusted.replace("FROM latest f\n\tLEFT", "FROM normalized f\n\tLEFT", 1)
adjusted = adjusted.replace("CASE WHEN upper(f.cost_currency)='USD'", "CASE WHEN f.normalized_cost_currency='USD'")
adjusted = adjusted.replace("f.cost_amount, NULLIF(upper(btrim(f.cost_currency)),''),", "f.cost_amount, f.normalized_cost_currency,")
adjusted = adjusted.replace("ON CONFLICT (tenant_id, request_id, occurred_at)", "ON CONFLICT (request_id, occurred_at)", 1)
adjusted = adjusted.replace("DELETE FROM public.request_metrics_projection m USING keys k", "DELETE FROM public.request_metrics m USING keys k", 1)
adjusted = adjusted.replace(
    "(m.tenant_id,m.request_id,m.occurred_at)=(k.tenant_id,k.request_id,k.occurred_at)",
    "(m.tenant_id,m.fact_request_id,m.occurred_at)=(k.tenant_id,k.request_id,k.occurred_at) AND m.source_fact_id IS NOT NULL",
    1,
)
adjusted = adjusted.replace("), head AS (", """
), acknowledged_released AS (
    DELETE FROM public.request_metrics_pending q USING released_pending p
    WHERE (q.fact_id,q.occurred_at)=(p.fact_id,p.occurred_at) RETURNING 1
), head AS (""", 1)
adjusted = adjusted.replace(
    "(SELECT count(*) FROM acknowledged)),",
    "(SELECT count(*) FROM acknowledged),(SELECT count(*) FROM acknowledged_released)),",
    1,
)
assert "FROM normalized f" in adjusted and "FROM latest f" in normalized
assert "SET enqueued_at = request_metrics_refresh_pending.enqueued_at" in events

parts = [
 r'''
\set ON_ERROR_STOP on
\timing on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';
DO $$
BEGIN
  IF current_database() <> 'proof_baseline' OR
     NOT EXISTS (SELECT FROM public.schema_migrations WHERE version='973') OR
     to_regclass('public.request_metrics_projection') IS NOT NULL OR
     NOT (SELECT backfill_complete FROM public.request_metrics_projection_state WHERE singleton)
  THEN RAISE EXCEPTION 'expected the warmed 973 proof fixture'; END IF;
END $$;
SELECT singleton FROM public.request_metrics_projection_state WHERE singleton FOR UPDATE;
ALTER TABLE public.request_metrics
    ALTER COLUMN prompt_tokens TYPE bigint,
    ALTER COLUMN completion_tokens TYPE bigint,
    ALTER COLUMN cached_tokens TYPE bigint,
    ALTER COLUMN reasoning_tokens TYPE bigint,
    ALTER COLUMN total_tokens TYPE bigint,
    ALTER COLUMN image_tokens TYPE bigint,
    ALTER COLUMN audio_tokens TYPE bigint,
    ALTER COLUMN video_tokens TYPE bigint,
    ALTER COLUMN latency_ms TYPE bigint,
    ALTER COLUMN ttft_ms TYPE bigint;
DO $$
DECLARE columns text;
BEGIN
  SELECT string_agg(format('physical.%I',a.attname),', ' ORDER BY a.attnum) INTO columns
  FROM pg_attribute a WHERE a.attrelid = 'public.request_metrics'::regclass AND a.attnum>0 AND NOT a.attisdropped
       AND a.attname NOT IN ('request_id','fact_request_id');
  EXECUTE 'CREATE VIEW public.request_metrics_projection AS
    SELECT physical.fact_request_id AS request_id, ' || columns || '
    FROM public.request_metrics physical WHERE physical.source_fact_id IS NOT NULL';
END $$;
''',
queue, events, daily, index,
f'PREPARE released_batch(integer) AS\n{released_batch};',
'PREPARE bridge_batch(integer) AS\n'+adjusted+';',
r'''
DO $$
BEGIN
 IF (SELECT count(*) FROM public.request_metrics_projection) <> 68469 OR
    (SELECT count(*) FROM public.request_metrics WHERE source_fact_id IS NULL) <> 68140
 THEN RAISE EXCEPTION 'original 973 projected or legacy rows changed'; END IF;
 IF EXISTS (
    (SELECT tenant_id,request_id,occurred_at,source_fact_id,total_tokens,cost_amount,cost_currency FROM public.request_metrics_projection
     EXCEPT ALL
     SELECT tenant_id,fact_request_id,occurred_at,source_fact_id,total_tokens,cost_amount,cost_currency
     FROM public.request_metrics WHERE source_fact_id IS NOT NULL)
 ) THEN RAISE EXCEPTION 'view identity or measures differ'; END IF;
END $$;

INSERT INTO public.request_metrics (request_id,tenant_id,occurred_at,occurred_date,billing_date,total_tokens,status,source)
VALUES ('v2-973-bridge-shared','fixture-973-a','2026-11-23 02:50:23+08','2026-11-22','2026-11-22',7,'success','async_processor');
INSERT INTO public.usage_facts (event_id,request_id,tenant_id,occurred_at,revision,status,total_tokens,cost_amount,cost_currency)
VALUES
 ('v2-973-bridge-a','v2-973-bridge-shared','fixture-973-a','2026-11-23 02:50:23+08',1,'success',10,0.01,'USD'),
 ('v2-973-bridge-b','v2-973-bridge-shared','fixture-973-b','2026-11-23 02:50:23+08',1,'success',12,0.02,'CNY');
EXECUTE released_batch(500);
DO $$
BEGIN
 IF (SELECT count(*) FROM public.request_metrics_projection WHERE request_id='v2-973-bridge-shared') <> 2 OR
    (SELECT count(*) FROM public.request_metrics_pending) <> 0 OR
    (SELECT count(*) FROM public.request_metrics_refresh_pending) <> 2
 THEN RAISE EXCEPTION 'released slot must project cross-tenant insertions and acknowledge only its queue'; END IF;
END $$;

UPDATE public.request_metrics_daily_state SET dirty=false WHERE day='2026-11-23';
UPDATE public.usage_facts SET total_tokens=3000000000,cost_amount=0.125,cost_currency='CNY'
WHERE event_id='v2-973-bridge-a';
INSERT INTO public.request_context_attrs (request_id,tenant_id,ts,gw_session_id)
VALUES ('v2-973-bridge-shared','fixture-973-a','2026-11-23 02:54:23+08','v2-973-bridge-session');
DELETE FROM public.usage_facts WHERE event_id='v2-973-bridge-b';

DO $$
BEGIN
 IF (SELECT dirty FROM public.request_metrics_daily_state WHERE day='2026-11-23') OR
    (SELECT count(*) FROM public.request_metrics WHERE request_id='v2-973-bridge-shared' AND source_fact_id IS NULL AND total_tokens=7) <> 1
 THEN RAISE EXCEPTION 'pending repair or legacy insert changed published rows'; END IF;
END $$;
EXECUTE bridge_batch(500);
EXECUTE released_batch(500);
DO $$
BEGIN
 IF (SELECT count(*) FROM public.request_metrics_projection WHERE request_id='v2-973-bridge-shared') <> 1 OR
    NOT EXISTS (SELECT FROM public.request_metrics_projection WHERE request_id='v2-973-bridge-shared'
                AND tenant_id='fixture-973-a' AND total_tokens=3000000000 AND cost_amount=0.125
                AND cost_currency='CNY' AND session_id='v2-973-bridge-session') OR
    (SELECT count(*) FROM public.request_metrics WHERE request_id='v2-973-bridge-shared' AND source_fact_id IS NULL AND total_tokens=7) <> 1 OR
    NOT (SELECT dirty FROM public.request_metrics_daily_state WHERE day='2026-11-23') OR
    EXISTS (SELECT FROM public.request_metrics_refresh_pending) OR EXISTS (SELECT FROM public.request_metrics_pending)
 THEN RAISE EXCEPTION 'repair missed wide revision, late context, deletion, day invalidation or queue acknowledgement'; END IF;
END $$;
SELECT 'pass' AS postgres_973_compatible_repair, (SELECT count(*) FROM public.request_metrics_projection) AS facts;
ROLLBACK;
''']
proof = "\n\n".join(parts).lstrip()
if not options.with_jit:
    proof = proof.replace("BEGIN;", "BEGIN;\nSET LOCAL jit = off;", 1)
if options.explain:
    proof = proof.replace(
        "EXECUTE bridge_batch(500);",
        "EXPLAIN (ANALYZE, BUFFERS, SETTINGS) EXECUTE bridge_batch(500);",
        1,
    )
print(proof)
