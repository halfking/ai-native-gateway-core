-- 415_restore_node_probe_runs.sql
-- Purpose: Restore the node-probe audit table when migration 341 was absent
-- from a database that already contains node_probe_state.
-- Idempotent: YES
-- Rollback: DROP TABLE IF EXISTS node_probe_runs;

CREATE TABLE IF NOT EXISTS node_probe_runs (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    credential_id       bigint NOT NULL,
    raw_model_name      text NOT NULL,
    trigger_kind        text NOT NULL,
    trigger_request_id  text,
    attempt             integer NOT NULL,
    next_retry_seconds  integer NOT NULL,
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
    success             boolean NOT NULL,
    started_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz,
    duration_ms         integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
        trigger_kind IN ('request_failure', 'manual', 'credential_recovery')
    ),
    CONSTRAINT node_probe_runs_attempt_check CHECK (attempt BETWEEN 1 AND 7)
);

CREATE INDEX IF NOT EXISTS idx_node_probe_runs_cred_model
    ON node_probe_runs (credential_id, raw_model_name, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_node_probe_runs_started
    ON node_probe_runs (started_at DESC);
