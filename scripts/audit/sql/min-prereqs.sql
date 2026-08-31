-- audit/min-prereqs.sql
-- Minimum preconditions so the 627-631 migration SQL can be applied to a
-- fresh, empty database. The real production path applies 511-626 first to
-- build the full schema, but for an audit-time smoke test we only need the
-- tables that 627-631 reference.
--
-- Apply with:
--   bash scripts/audit/psql-isolated.sh -f scripts/audit/sql/min-prereqs.sql

BEGIN;

-- get_current_tenant() is required by migration 630's FORCE RLS policy.
CREATE OR REPLACE FUNCTION public.get_current_tenant() RETURNS text
    LANGUAGE sql STABLE
    AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;

-- candidate_failure_logs (columnar parent + hot) — required by 627/628.
CREATE TABLE IF NOT EXISTS public.candidate_failure_logs (
    id bigserial,
    request_id text NOT NULL,
    ts timestamptz NOT NULL DEFAULT NOW(),
    tenant_id text NOT NULL,
    credential_id text,
    provider_id text,
    raw_model_name text,
    attempt_index integer NOT NULL DEFAULT 0,
    error_kind text,
    error_message text,
    upstream_status_code integer,
    upstream_response_body text,
    upstream_response_preview text,
    latency_ms integer,
    retryable boolean,
    per_attempt_latency_ms integer[],
    extracted_upstream_status_code integer,
    diagnosed_error_kind text,
    context jsonb,
    session_id text,
    aggregation_id bigint,
    partition_date date NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE TABLE IF NOT EXISTS public.candidate_failure_logs_hot (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    ts timestamptz NOT NULL DEFAULT NOW(),
    tenant_id text NOT NULL,
    credential_id text,
    provider_id text,
    raw_model_name text,
    attempt_index integer NOT NULL DEFAULT 0,
    error_kind text,
    error_message text,
    upstream_status_code integer,
    upstream_response_body text,
    upstream_response_preview text,
    latency_ms integer,
    retryable boolean,
    per_attempt_latency_ms integer[],
    extracted_upstream_status_code integer,
    diagnosed_error_kind text,
    context jsonb,
    session_id text,
    aggregation_id bigint
);

-- provider_error_aggregator_state — referenced by 627 watermark reseed.
CREATE TABLE IF NOT EXISTS public.provider_error_aggregator_state (
    id integer PRIMARY KEY DEFAULT 1,
    last_source_id bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_error_aggregator_state_singleton CHECK (id = 1)
);

-- credentials + providers — required by 631's status CHECK expansion.
CREATE TABLE IF NOT EXISTS public.credentials (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active'
);
CREATE TABLE IF NOT EXISTS public.providers (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    deleted_at timestamptz
);

-- public.sessions — used by the reaper integration tests. The real
-- production schema ships this as a partitioned table with many columns;
-- for the audit fixture we need only the columns the aggregator and the
-- reaper assertions read.
CREATE TABLE IF NOT EXISTS public.sessions (
    tenant_id   text        NOT NULL,
    session_id  text        NOT NULL,
    partition_date date     NOT NULL,
    total_turns integer     NOT NULL DEFAULT 0,
    total_tokens bigint     NOT NULL DEFAULT 0,
    total_cost_usd numeric  NOT NULL DEFAULT 0,
    last_model  text,
    last_provider text,
    last_request_summary text,
    last_response_summary text,
    updated_at  timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, session_id, partition_date)
);

-- session_turns_hot — used by reaper test 2 (source session present). The
-- real production schema includes many more columns; the audit fixture
-- only needs the rows the aggregator claims.
CREATE TABLE IF NOT EXISTS public.session_turns_hot (
    id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    session_id text NOT NULL,
    request_id text NOT NULL,
    turn_no integer NOT NULL,
    partition_date date NOT NULL DEFAULT CURRENT_DATE,
    aggregate_applied_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_request
    ON public.session_turns_hot (tenant_id, request_id, partition_date);

-- request_logs_hot — required by attachment cleanup execute (migration 629).
-- Production carries many more columns; the audit fixture only needs the
-- JSONB attachments column plus the ts/tenant_id/request_id columns the
-- cleanup SQL filters on.
CREATE TABLE IF NOT EXISTS public.request_logs_hot (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    ts timestamptz NOT NULL DEFAULT NOW(),
    tenant_id text NOT NULL DEFAULT 'default',
    success boolean NOT NULL DEFAULT true,
    attachments jsonb,
    client_model text
);

-- audit_attachments_filesystem_cleanup — defined in 632, listed here for
-- idempotent bootstrap so the attachment integration tests have it before
-- 632's IF NOT EXISTS bootstrap runs.
CREATE TABLE IF NOT EXISTS public.audit_attachments_filesystem_cleanup (
    id              bigserial PRIMARY KEY,
    cleanup_run_id  uuid        NOT NULL,
    tenant_id       text        NOT NULL,
    request_id      text        NOT NULL,
    file_path       text        NOT NULL,
    file_hash       text,
    file_size       bigint,
    file_mtime      timestamptz,
    cleaned_at      timestamptz NOT NULL DEFAULT NOW(),
    triggered_by_user text      NOT NULL,
    reason          text,
    CONSTRAINT audit_attachments_filesystem_cleanup_unique
        UNIQUE (request_id, file_path, cleanup_run_id)
);

-- ensure_candidate_failure_logs_partition — required by 628's promote
-- function. Copied verbatim from sql/migrations/startup/392 (the columnar
-- helper that creates monthly columnar partitions on demand). DROP first
-- so we get the right return type on re-apply.
DROP FUNCTION IF EXISTS public.ensure_candidate_failure_logs_partition(timestamp with time zone);
CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition(target_ts timestamp with time zone)
    RETURNS void
    LANGUAGE plpgsql
    AS $func$
DECLARE
    month_start date := date_trunc('month', target_ts)::date;
    month_end   date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := format('candidate_failure_logs_%s', to_char(month_start, 'YYYYMM'));
BEGIN
    IF to_regclass(format('public.%I', partition_name)) IS NOT NULL THEN
        RETURN;
    END IF;
    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.candidate_failure_logs
         FOR VALUES FROM (%L) TO (%L) USING columnar',
        partition_name, month_start, month_end
    );
    RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as columnar', partition_name;
END
$func$;

COMMIT;
