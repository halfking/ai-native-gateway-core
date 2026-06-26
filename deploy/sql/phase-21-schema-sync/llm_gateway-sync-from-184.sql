-- ============================================================
-- Sync SQL for database: llm_gateway
-- Generated: 2026-06-26 (Phase 21 schema reconciliation)
-- Source: 184 (production reference)
-- Tables missing in local: 12
-- ============================================================

\connect llm_gateway

CREATE TABLE public.credit_ledger_old (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    entry_type character varying(32) NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying(32),
    ref_id character varying(128),
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying(32),
    CONSTRAINT credit_ledger_entry_type_check CHECK (((entry_type)::text = ANY (ARRAY[('consume'::character varying)::text, ('topup'::character varying)::text, ('subscribe'::character varying)::text, ('adjust'::character varying)::text, ('refund'::character varying)::text])))
);

CREATE TABLE public.tool_usage_stats_old (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    usage_date date DEFAULT CURRENT_DATE NOT NULL,
    call_count bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

COMMENT ON COLUMN public.tool_usage_stats_old.call_count IS 'Total call count for this tool on this day';

COMMENT ON COLUMN public.tool_usage_stats_old.success_count IS 'Successful call count';

COMMENT ON COLUMN public.tool_usage_stats_old.error_count IS 'Failed call count';

CREATE TABLE public.usage_ledger_old (
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id integer,
    api_key_id integer,
    end_user_id text,
    credential_id integer,
    provider_id integer,
    canonical_id integer,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    cache_read_tokens integer,
    cache_write_tokens integer,
    total_tokens integer,
    cost_usd numeric(12,6),
    latency_ms integer,
    success boolean,
    error_kind text
);

CREATE INDEX idx_credit_ledger_tenant_ts ON public.credit_ledger_old USING btree (tenant_id, created_at DESC);

CREATE INDEX idx_tool_usage_stats_date ON public.tool_usage_stats_old USING btree (usage_date DESC);

CREATE INDEX idx_tool_usage_stats_tenant_id ON public.tool_usage_stats_old USING btree (tenant_id);

CREATE INDEX idx_tool_usage_stats_tool_id ON public.tool_usage_stats_old USING btree (tool_id);

CREATE INDEX idx_tool_usage_stats_tool_tenant ON public.tool_usage_stats_old USING btree (tool_id, tenant_id, usage_date DESC);

ALTER TABLE public.credit_ledger_old ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_credit_ledger ON public.credit_ledger_old USING (((tenant_id)::text = public.get_current_tenant()));

CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats_old USING (((tenant_id)::text = public.get_current_tenant()));

ALTER TABLE public.tool_usage_stats_old ENABLE ROW LEVEL SECURITY;
