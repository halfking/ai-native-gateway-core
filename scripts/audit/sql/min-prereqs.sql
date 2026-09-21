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

-- session_turns_hot — used by the reaper integration tests AND by the
-- audit-only promote-session-turns smoke test. Production migration 526
-- creates a 50+ column schema that depends on request_logs.gw_session_id
-- + a parallel session_turns write-ahead path which is too heavy to
-- bootstrap in an isolated audit container. We register a minimum-viable
-- audit-only hot table here; the audit-only promote helper in
-- scripts/audit/verify-promote-session-bodies-turns.sh exercises the
-- same DELETE-RETURNING + INSERT pattern that 526 uses for the
-- production column set.
-- DROP CASCADE because 626's session_bodies_unified view references
-- session_bodies_hot — the view is recreated by 625 below.
DROP TABLE IF EXISTS public.session_turns_hot CASCADE;
CREATE TABLE public.session_turns_hot (
    id bigserial PRIMARY KEY,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    ts timestamptz NOT NULL DEFAULT NOW(),
    partition_date date NOT NULL DEFAULT CURRENT_DATE,
    submit_mode text NOT NULL DEFAULT 'full',
    model text,
    provider text,
    prompt_tokens integer DEFAULT 0,
    completion_tokens integer DEFAULT 0
);

-- session_bodies_hot — required by promote_session_bodies_hot_to_partition
-- (614/615/626). The promotion integration test seeds 50 rows here and
-- asserts hot=0 / parent=50 / unified=50 after the promote. We register
-- the production-shape 12-column schema (matches 614's CREATE TABLE).
-- DROP CASCADE because session_bodies_unified (from 614/625) depends on
-- this table; the view is re-created by 625 below.
DROP TABLE IF EXISTS public.session_bodies_hot CASCADE;
CREATE TABLE public.session_bodies_hot (
    id bigserial PRIMARY KEY,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamptz NOT NULL DEFAULT NOW(),
    partition_date date NOT NULL DEFAULT CURRENT_DATE
);

-- session_bodies (partitioned parent) — required as a promote target
-- by 614/615/626. Production schema has ~46 columns; the audit fixture
-- carries the minimum column subset that promote_session_bodies_hot_to_partition
-- INSERTs into. We PARTITION BY RANGE (partition_date) and create one
-- catch-all default partition so any date is writable.
-- DROP CASCADE because session_bodies_unified (from 614/625) depends on
-- the parent table; 625 re-creates the view below.
DROP TABLE IF EXISTS public.session_bodies CASCADE;
CREATE TABLE public.session_bodies (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    request_delta jsonb,
    response_delta jsonb,
    outbound_body jsonb,
    request_attachments jsonb DEFAULT '[]'::jsonb,
    response_attachments jsonb DEFAULT '[]'::jsonb,
    ts timestamptz NOT NULL DEFAULT NOW(),
    partition_date date NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE TABLE public.session_bodies_default PARTITION OF public.session_bodies DEFAULT;

-- session_turns (partitioned parent) — required as a promote target by
-- 526 (and the audit-only promote helper). Same minimum-column pattern
-- as session_bodies above.
DROP TABLE IF EXISTS public.session_turns CASCADE;
CREATE TABLE public.session_turns (
    id bigint NOT NULL,
    session_id text NOT NULL,
    turn_no integer NOT NULL,
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    ts timestamptz NOT NULL DEFAULT NOW(),
    partition_date date NOT NULL DEFAULT CURRENT_DATE,
    submit_mode text NOT NULL DEFAULT 'full',
    model text,
    provider text,
    prompt_tokens integer DEFAULT 0,
    completion_tokens integer DEFAULT 0,
    PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE TABLE public.session_turns_default PARTITION OF public.session_turns DEFAULT;

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
