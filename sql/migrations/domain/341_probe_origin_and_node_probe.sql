-- Migration 341: probe / self-check origin tagging + node_probe schema
--
-- Goals (per "重新讨论自检、自动探测的规范" 2026-07-14):
--   1. request_logs: add origin_stage + origin_actor columns so the realtime
--      stream can label every row as self_check | node_probe | system_health
--      | business.  Older probe_* values remain valid (CHECK is additive)
--      so we do not break rows already written by credential_probe_v2 /
--      model_probe / active_probe / passive_probe.
--   2. client_ip / client_forwarded_for on request_logs_hot: the columns
--      were added by deploy/sql/migrations/2026-07-11-observability-fields.sql
--      but every row in the last 24h on 252 was NULL — no writer existed.
--      This migration only restates the columns + a partial index; the
--      writer is added in middleware/origin_mw.go (commit 3).
--   3. node_probe_runs / node_probe_state: backing tables for the new
--      bg/node_probe.go worker (5s/30s/60s/5m/1h/2h/24h backoff ladder).
--   4. system_health_status(): SQL helper for the 30s windowed health
--      score consumed by bg/system_health.go and the GDRT H badge.
--   5. self_check_runs: extend status CHECK to include 'retrying'
--      (used by credential_selfcheck when 3 fallback models all fail).
--
-- Pattern reference: 2026-07-13-multimodal-token-fields-hot.sql uses
-- ADD COLUMN IF NOT EXISTS on request_logs_hot + parent, letting PG 11+
-- auto-propagate to monthly partitions. We do the same here.

BEGIN;

-- -------------------------------------------------------------------
-- 1. request_logs_hot + request_logs: origin_stage / origin_actor
-- -------------------------------------------------------------------
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS origin_stage VARCHAR(32) DEFAULT NULL,
    ADD COLUMN IF NOT EXISTS origin_actor VARCHAR(64) DEFAULT NULL;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS origin_stage VARCHAR(32) DEFAULT NULL,
    ADD COLUMN IF NOT EXISTS origin_actor VARCHAR(64) DEFAULT NULL;

COMMENT ON COLUMN request_logs_hot.origin_stage IS
'341: probe/self-check origin — self_check | node_probe | system_health | business | legacy probe_* values';
COMMENT ON COLUMN request_logs_hot.origin_actor IS
'341: worker / actor name (credential-selfcheck-worker, node-probe-worker, system-health-worker, manual:<id>)';

-- additive CHECK so legacy probe_direct / probe_v2 / model_probe / passive_probe / manual
-- values written by older code paths keep working.
ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_origin_stage_check;
ALTER TABLE request_logs
    ADD CONSTRAINT request_logs_origin_stage_check CHECK (
        origin_stage IS NULL OR origin_stage IN (
            'self_check', 'node_probe', 'system_health', 'business',
            'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'
        )
    );

-- partial index for the GDRT / realtime filter.
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_origin_stage_ts
    ON request_logs_hot (origin_stage, ts DESC)
    WHERE origin_stage IS NOT NULL;

-- -------------------------------------------------------------------
-- 2. client_ip / client_forwarded_for (restated for clarity)
-- -------------------------------------------------------------------
-- Already present from 2026-07-11-observability-fields.sql on
-- request_logs + request_logs_hot; the partial index there covers
-- the writer we will add in commit 3.  Re-stating the COMMENT here
-- so it shows up alongside the new origin columns in \d output.
COMMENT ON COLUMN request_logs_hot.client_ip IS
'341: writer is middleware/origin_mw.go (commit 3) — every business row must carry the real client IP';
COMMENT ON COLUMN request_logs_hot.client_forwarded_for IS
'341: full X-Forwarded-For chain (1024B truncation in origin_mw)';

-- -------------------------------------------------------------------
-- 3. node_probe_runs — audit log of every node-probe attempt
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS node_probe_runs (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    credential_id       bigint  NOT NULL,
    raw_model_name      text    NOT NULL,
    trigger_kind        text    NOT NULL,            -- request_failure | manual | credential_recovery
    trigger_request_id  text,
    attempt             integer NOT NULL,            -- 1..7
    next_retry_seconds  integer NOT NULL,            -- 5/30/60/300/3600/7200/86400
    direct_ok           boolean NOT NULL,
    direct_http_status  integer,
    direct_err_code     text,
    direct_latency_ms   integer,
    direct_err_detail   text,
    gateway_ok          boolean NOT NULL,
    gateway_http_status integer,
    gateway_err_code    text,
    gateway_latency_ms  integer,
    gateway_err_detail  text,
    success             boolean NOT NULL,            -- direct_ok AND gateway_ok
    started_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    duration_ms         integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
        trigger_kind IN ('request_failure','manual','credential_recovery')
    ),
    CONSTRAINT node_probe_runs_attempt_check CHECK (attempt BETWEEN 1 AND 7)
);

CREATE INDEX IF NOT EXISTS idx_node_probe_runs_cred_model
    ON node_probe_runs (credential_id, raw_model_name, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_node_probe_runs_started
    ON node_probe_runs (started_at DESC);

COMMENT ON TABLE node_probe_runs IS
'341: audit log of every node probe attempt (direct + gateway round). Paused after attempt=7 with next_retry_seconds=86400.';
COMMENT ON COLUMN node_probe_runs.next_retry_seconds IS
'341: seconds until next attempt — 5/30/60/300/3600/7200/86400 per spec ladder';

-- -------------------------------------------------------------------
-- 4. node_probe_state — consensus + backoff state per (cred, model)
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS node_probe_state (
    credential_id            bigint NOT NULL,
    raw_model_name           text   NOT NULL,
    consecutive_failures     integer NOT NULL DEFAULT 0,
    consecutive_successes    integer NOT NULL DEFAULT 0,
    last_attempt_at          timestamptz,
    next_retry_at            timestamptz NOT NULL DEFAULT now(),
    next_retry_seconds       integer NOT NULL DEFAULT 5,
    paused                   boolean NOT NULL DEFAULT false,    -- true after attempt 7
    last_run_id              bigint,
    last_direct_ok           boolean,
    last_gateway_ok          boolean,
    last_err_code            text,
    last_err_detail          text,
    in_flight_until          timestamptz,                       -- prevents same-key re-entry
    updated_at               timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_node_probe_state_due
    ON node_probe_state (next_retry_at)
    WHERE paused = FALSE;

COMMENT ON TABLE node_probe_state IS
'341: per (credential, model) node-probe state machine. 7-step backoff ladder, paused after attempt=7 (24h cap).';

-- -------------------------------------------------------------------
-- 5. system_health_status() — 30s windowed success rate
-- -------------------------------------------------------------------
CREATE OR REPLACE FUNCTION system_health_status(
    p_window_seconds integer DEFAULT 30
) RETURNS TABLE (
    status          text,
    success_rate    numeric,
    sample_count    bigint,
    failure_count   bigint,
    last_check_at   timestamptz
)
LANGUAGE SQL
STABLE
AS $$
    WITH win AS (
        SELECT
            COUNT(*)::bigint                AS n,
            COUNT(*) FILTER (WHERE success)::bigint AS ok,
            COUNT(*) FILTER (WHERE NOT success)::bigint AS fail
        FROM request_logs_hot
        WHERE ts >= now() - make_interval(secs => p_window_seconds)
    )
    SELECT
        CASE
            WHEN n = 0                                  THEN 'suspect'
            WHEN (ok::numeric / NULLIF(n,0)) >= 0.80    THEN 'ok'
            ELSE 'degraded'
        END                                            AS status,
        ROUND( (ok::numeric / NULLIF(n,0))::numeric, 4) AS success_rate,
        n                                              AS sample_count,
        fail                                           AS failure_count,
        now()                                          AS last_check_at
    FROM win;
$$;

COMMENT ON FUNCTION system_health_status(integer) IS
'341: returns ok (>=80% success), degraded (<80%), or suspect (no traffic) over a sliding window. Consumed by bg/system_health.go and /api/health/system.';

-- -------------------------------------------------------------------
-- 6. self_check_runs: extend status CHECK with 'retrying'
-- -------------------------------------------------------------------
ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_status_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_status_check CHECK (
        status IN ('running','success','partial','failed','retrying')
    );

-- selection_strategy + attempted_models: how credential_selfcheck picked
-- the model. Reused for forensic analysis of failed daily checks.
ALTER TABLE self_check_runs
    ADD COLUMN IF NOT EXISTS selection_strategy text DEFAULT 'most_used',
    ADD COLUMN IF NOT EXISTS attempted_models   jsonb DEFAULT '[]'::jsonb;

COMMENT ON COLUMN self_check_runs.selection_strategy IS
'341: most_used | fallback_<n> | random — which model the credential_selfcheck worker tested';
COMMENT ON COLUMN self_check_runs.attempted_models IS
'341: ordered list of models tried during this run, e.g. ["gpt-4o","gpt-4o-mini","claude-haiku-4-5"]';

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL OR selection_strategy LIKE 'most_used'
                                                       OR selection_strategy LIKE 'fallback_%'
                                                       OR selection_strategy = 'random'
    );

-- -------------------------------------------------------------------
-- 7. helper: a credential's most-used model in the last 24h
-- -------------------------------------------------------------------
CREATE OR REPLACE FUNCTION credential_most_used_model(
    p_credential_id bigint,
    p_window_hours  integer DEFAULT 24
) RETURNS TABLE (
    raw_model_name text,
    call_count     bigint
)
LANGUAGE SQL
STABLE
AS $$
    SELECT pm.raw_model_name, COUNT(*) AS call_count
    FROM request_logs_hot rl
    JOIN provider_models pm ON pm.id = rl.canonical_id
    WHERE rl.credential_id = p_credential_id
      AND rl.ts >= now() - make_interval(hours => p_window_hours)
      AND rl.success = TRUE
    GROUP BY pm.raw_model_name
    ORDER BY call_count DESC, pm.raw_model_name ASC
    LIMIT 1;
$$;

COMMENT ON FUNCTION credential_most_used_model(bigint, integer) IS
'341: top-1 model by 24h successful traffic for a credential. Used by credential_selfcheck to pick the daily probe model.';

COMMIT;
