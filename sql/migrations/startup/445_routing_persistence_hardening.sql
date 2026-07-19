-- 2026-07-14 routing persistence hardening
--
-- Purpose:
--   Synchronize the schema required by the routing incident audit path and
--   the hot persistence writers used by the gateway.
--
-- Apply order:
--   1. Run this file against the target llm_gateway database.
--   2. Restart the gateway only after the verification block succeeds.
--   3. Keep the output and schema_migrations row with the deployment record.
--
-- This migration is idempotent and is intended for both local validation and
-- the later 252 synchronization. It does not drop data or rewrite tables.

BEGIN;

-- 1. Ensure hot write targets exist before adding columns used by writers.
DO $$
BEGIN
    IF to_regclass('public.request_logs_hot') IS NULL
       AND to_regclass('public.request_logs') IS NOT NULL THEN
        EXECUTE 'CREATE TABLE public.request_logs_hot (LIKE public.request_logs INCLUDING ALL)';
    END IF;
    IF to_regclass('public.usage_ledger_hot') IS NULL
       AND to_regclass('public.usage_ledger') IS NOT NULL THEN
        EXECUTE 'CREATE TABLE public.usage_ledger_hot (LIKE public.usage_ledger INCLUDING ALL)';
    END IF;
    IF to_regclass('public.request_wal_hot') IS NULL
       AND to_regclass('public.request_wal') IS NOT NULL THEN
        EXECUTE 'CREATE TABLE public.request_wal_hot (LIKE public.request_wal INCLUDING ALL)';
    END IF;
    IF to_regclass('public.routing_decision_log_hot') IS NULL
       AND to_regclass('public.routing_decision_log') IS NOT NULL THEN
        EXECUTE 'CREATE TABLE public.routing_decision_log_hot (LIKE public.routing_decision_log INCLUDING ALL)';
    END IF;
END $$;

-- 2. Keep hot request-log columns aligned with the parent and current writers.
ALTER TABLE IF EXISTS public.request_logs_hot
    ADD COLUMN IF NOT EXISTS reasoning_tokens INT,
    ADD COLUMN IF NOT EXISTS image_tokens INT,
    ADD COLUMN IF NOT EXISTS audio_tokens INT,
    ADD COLUMN IF NOT EXISTS video_tokens INT,
    ADD COLUMN IF NOT EXISTS provider_tokens INT,
    ADD COLUMN IF NOT EXISTS client_request_id TEXT,
    ADD COLUMN IF NOT EXISTS upstream_status_code INTEGER,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS stream_chunk_errors INTEGER,
    ADD COLUMN IF NOT EXISTS stream_chunks_sent INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attachments JSONB;

ALTER TABLE IF EXISTS public.usage_ledger_hot
    ADD COLUMN IF NOT EXISTS reasoning_tokens INT,
    ADD COLUMN IF NOT EXISTS image_tokens INT,
    ADD COLUMN IF NOT EXISTS audio_tokens INT,
    ADD COLUMN IF NOT EXISTS video_tokens INT,
    ADD COLUMN IF NOT EXISTS provider_tokens INT;

-- 3. Ensure indexes required by current hot-table readers and writers.
DO $$
BEGIN
    IF to_regclass('public.request_logs_hot') IS NOT NULL THEN
        CREATE INDEX IF NOT EXISTS idx_request_logs_hot_ts
            ON public.request_logs_hot (ts DESC);
        CREATE INDEX IF NOT EXISTS idx_request_logs_hot_tenant_ts
            ON public.request_logs_hot (tenant_id, ts DESC);
        CREATE INDEX IF NOT EXISTS idx_request_logs_hot_multimodal_usage
            ON public.request_logs_hot (tenant_id, ts DESC)
            WHERE image_tokens > 0 OR audio_tokens > 0 OR video_tokens > 0;
    END IF;
    IF to_regclass('public.request_wal_hot') IS NOT NULL THEN
        CREATE INDEX IF NOT EXISTS idx_request_wal_hot_created_at
            ON public.request_wal_hot (created_at DESC);
        CREATE INDEX IF NOT EXISTS idx_request_wal_hot_tenant_created
            ON public.request_wal_hot (tenant_id, created_at DESC);
    END IF;
    IF to_regclass('public.routing_decision_log_hot') IS NOT NULL THEN
        CREATE INDEX IF NOT EXISTS idx_routing_decision_log_hot_ts
            ON public.routing_decision_log_hot (ts DESC);
        CREATE INDEX IF NOT EXISTS idx_routing_decision_log_hot_tenant_ts
            ON public.routing_decision_log_hot (tenant_id, ts DESC);
        CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_decision_log_hot_request_ts
            ON public.routing_decision_log_hot (request_id, ts);
    END IF;
END $$;

-- 4. Backfill the route-incident audit schema. This also repairs the legacy
--    8-column routing_audit_log shape without replacing existing rows.
ALTER TABLE IF EXISTS public.routing_audit_log
    ADD COLUMN IF NOT EXISTS incident_id UUID,
    ADD COLUMN IF NOT EXISTS tenant_id TEXT,
    ADD COLUMN IF NOT EXISTS confirmation_token_hash TEXT,
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT,
    ADD COLUMN IF NOT EXISTS request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS pre_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS post_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS response_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS outcome TEXT,
    ADD COLUMN IF NOT EXISTS failure_reason TEXT,
    ADD COLUMN IF NOT EXISTS diagnostic_run_id UUID,
    ADD COLUMN IF NOT EXISTS actor_ip_hash TEXT,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE public.routing_audit_log
   SET tenant_id = 'default'
 WHERE tenant_id IS NULL;

DO $$
BEGIN
    IF to_regclass('public.routing_audit_log') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
            WHERE conrelid = 'public.routing_audit_log'::regclass
              AND conname = 'routing_audit_log_action_check'
       ) THEN
        ALTER TABLE public.routing_audit_log
          ADD CONSTRAINT routing_audit_log_action_check
          CHECK (action IS NOT NULL AND length(action) > 0);
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_audit_log_idempotency
    ON public.routing_audit_log (idempotency_key);
CREATE INDEX IF NOT EXISTS idx_routing_audit_log_tenant_ts
    ON public.routing_audit_log (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_routing_audit_log_incident
    ON public.routing_audit_log (incident_id);
CREATE INDEX IF NOT EXISTS idx_routing_audit_log_action
    ON public.routing_audit_log (action, ts DESC);

CREATE TABLE IF NOT EXISTS public.diagnostic_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    kind TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    trigger_source TEXT,
    error TEXT,
    summary_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE public.diagnostic_runs
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_tenant_started
    ON public.diagnostic_runs (tenant_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_created
    ON public.diagnostic_runs (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_incident
    ON public.diagnostic_runs (incident_id);
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_state
    ON public.diagnostic_runs (state) WHERE state IN ('pending', 'running');

CREATE TABLE IF NOT EXISTS public.approval_routing_rules (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    rule_name TEXT NOT NULL,
    rule_type TEXT NOT NULL,
    conditions JSONB NOT NULL DEFAULT '{}'::jsonb,
    approvers JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_approval_routing_rules_tenant
    ON public.approval_routing_rules (tenant_id, enabled);

DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        INSERT INTO public.schema_migrations (version, description)
        VALUES (
            '2026-07-14-routing-persistence-hardening',
            'Align hot persistence writers and route incident audit schema'
        ) ON CONFLICT DO NOTHING;
    END IF;
END $$;

-- 5. Verification is part of the migration. Any missing critical object
--    aborts the transaction so a target cannot be left half-updated.
DO $$
BEGIN
    IF to_regclass('public.request_logs_hot') IS NULL THEN
        RAISE EXCEPTION 'request_logs_hot is missing';
    END IF;
    IF to_regclass('public.request_wal_hot') IS NULL THEN
        RAISE EXCEPTION 'request_wal_hot is missing';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name = 'request_logs_hot'
           AND column_name = 'upstream_status_code'
    ) THEN
        RAISE EXCEPTION 'request_logs_hot.upstream_status_code is missing';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name = 'routing_audit_log'
           AND column_name = 'idempotency_key'
    ) THEN
        RAISE EXCEPTION 'routing_audit_log.idempotency_key is missing';
    END IF;
END $$;

COMMIT;
