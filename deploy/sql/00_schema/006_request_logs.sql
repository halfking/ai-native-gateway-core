-- ============================================================================
-- llm-gateway-go Request Logs 分区表结构
-- ============================================================================
-- 注意：request_logs采用按月分区，当前月份使用heap存储，历史月份归档为columnar
-- ============================================================================

-- ----------------------------------------------------------------------------
-- get_current_tenant() 函数 - 获取当前租户的会话变量
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.get_current_tenant()
RETURNS text
LANGUAGE sql
STABLE
AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;

-- ----------------------------------------------------------------------------
-- Request Logs 分区父表（heap存储，当前月份数据）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_logs (
    id bigint NOT NULL DEFAULT nextval('request_logs_id_seq'::regclass),
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    stream_chunk_errors integer,
    stream_done_sent boolean,
    client_timeout boolean,
    client_endpoint text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    stream_interrupted boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    client_request_id text,
    PRIMARY KEY (id, ts),
    CONSTRAINT chk_compression_parent_single CHECK (parent_request_id IS NULL OR compression_reason IS NOT NULL),
    CONSTRAINT request_logs_strategy_used_check CHECK (strategy_used IS NULL OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text])))
) PARTITION BY RANGE (ts);

-- 序列（用于id自增）
CREATE SEQUENCE IF NOT EXISTS public.request_logs_id_seq;

-- ----------------------------------------------------------------------------
-- Request Logs 分区表（heap存储，当前月份）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_logs_2026_07
    PARTITION OF public.request_logs
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

CREATE TABLE IF NOT EXISTS public.request_logs_2026_08
    PARTITION OF public.request_logs
    FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');

-- 默认分区（捕获不符合其他分区范围的数据）
CREATE TABLE IF NOT EXISTS public.request_logs_default
    PARTITION OF public.request_logs DEFAULT;

-- ----------------------------------------------------------------------------
-- Request Logs 索引
-- ----------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts ON public.request_logs (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_credential_ts ON public.request_logs (credential_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_ts ON public.request_logs (provider_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_client_model_trgm ON public.request_logs USING gin (client_model gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id_ts_unique ON public.request_logs (request_id, ts);
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_ts ON public.request_logs (gw_session_id, ts DESC) WHERE gw_session_id IS NOT NULL AND gw_session_id <> ''::text;
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_task_ts ON public.request_logs (gw_task_id, ts DESC) WHERE gw_task_id IS NOT NULL AND gw_task_id <> ''::text;
CREATE INDEX IF NOT EXISTS idx_request_logs_status_ts ON public.request_logs (request_status, ts DESC) WHERE request_status IS NOT NULL AND request_status <> ''::text;
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_ts ON public.request_logs (parent_request_id, ts DESC) WHERE parent_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_client_request_id ON public.request_logs (client_request_id, ts DESC) WHERE client_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_session_outbound ON public.request_logs (gw_session_id, ts DESC) WHERE gw_session_id IS NOT NULL AND outbound_body IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_outbound_msg_count ON public.request_logs (tenant_id, ts DESC) WHERE outbound_msg_count IS NOT NULL AND outbound_msg_count > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_quality_flags ON public.request_logs USING gin (quality_flags) WHERE cardinality(quality_flags) > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_quality ON public.request_logs (provider_id, quality_score, ts DESC) WHERE quality_score IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_finish_reason ON public.request_logs (upstream_finish_reason, ts DESC) WHERE upstream_finish_reason IS NOT NULL AND upstream_finish_reason <> ''::text;
CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls ON public.request_logs USING gin (tool_calls) WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls ON public.request_logs (provider_id, ts DESC) WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_work_type ON public.request_logs (work_type, ts DESC) WHERE work_type IS NOT NULL AND work_type <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged ON public.request_logs (tenant_id, ts DESC) WHERE credits_charged IS NOT NULL AND credits_charged > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_task_ts ON public.request_logs (tenant_id, gw_task_id, ts DESC) WHERE gw_task_id IS NOT NULL AND gw_task_id <> ''::text;

-- ----------------------------------------------------------------------------
-- Request Logs RLS（行级安全策略）
-- ----------------------------------------------------------------------------
ALTER TABLE public.request_logs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_request_logs ON public.request_logs;
CREATE POLICY tenant_isolation_request_logs ON public.request_logs
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Trigger: 自动更新 api_key_model_cost
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION update_api_key_model_cost()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.is_auto_request = TRUE THEN
        UPDATE api_key_model_cost
        SET call_count = call_count + 1,
            total_tokens = total_tokens + COALESCE(NEW.prompt_tokens, 0) + COALESCE(NEW.completion_tokens, 0),
            last_call_at = NEW.ts
        WHERE api_key_id = NEW.api_key_id
          AND model_name = COALESCE(NEW.outbound_model, NEW.client_model);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_update_api_key_model_cost ON request_logs;
CREATE TRIGGER trg_update_api_key_model_cost
    AFTER INSERT ON request_logs
    FOR EACH ROW WHEN (new.is_auto_request = true)
    EXECUTE FUNCTION update_api_key_model_cost();

-- ----------------------------------------------------------------------------
-- 添加 request_logs 的额外列（已在db.go中通过ALTER TABLE添加）
-- ----------------------------------------------------------------------------
DO $$
BEGIN
    -- 检查列是否存在，不存在则添加
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'gw_session_id') THEN
        ALTER TABLE request_logs ADD COLUMN gw_session_id TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'gw_task_id') THEN
        ALTER TABLE request_logs ADD COLUMN gw_task_id TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'request_status') THEN
        ALTER TABLE request_logs ADD COLUMN request_status TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'api_key_prefix') THEN
        ALTER TABLE request_logs ADD COLUMN api_key_prefix TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'api_key_owner_user') THEN
        ALTER TABLE request_logs ADD COLUMN api_key_owner_user TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'application_code') THEN
        ALTER TABLE request_logs ADD COLUMN application_code TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'parent_request_id') THEN
        ALTER TABLE request_logs ADD COLUMN parent_request_id TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'compression_reason') THEN
        ALTER TABLE request_logs ADD COLUMN compression_reason TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'compression_strategy') THEN
        ALTER TABLE request_logs ADD COLUMN compression_strategy TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'compression_meta') THEN
        ALTER TABLE request_logs ADD COLUMN compression_meta JSONB;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'outbound_body') THEN
        ALTER TABLE request_logs ADD COLUMN outbound_body JSONB;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'outbound_msg_count') THEN
        ALTER TABLE request_logs ADD COLUMN outbound_msg_count INT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'outbound_token_est') THEN
        ALTER TABLE request_logs ADD COLUMN outbound_token_est INT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'outbound_msg_hashes') THEN
        ALTER TABLE request_logs ADD COLUMN outbound_msg_hashes JSONB;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'quality_flags') THEN
        ALTER TABLE request_logs ADD COLUMN quality_flags TEXT[] NOT NULL DEFAULT '{}';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'quality_fix_actions') THEN
        ALTER TABLE request_logs ADD COLUMN quality_fix_actions JSONB NOT NULL DEFAULT '{}'::jsonb;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'quality_score') THEN
        ALTER TABLE request_logs ADD COLUMN quality_score NUMERIC(3,2);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'upstream_finish_reason') THEN
        ALTER TABLE request_logs ADD COLUMN upstream_finish_reason TEXT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'tool_calls') THEN
        ALTER TABLE request_logs ADD COLUMN tool_calls JSONB;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'request_logs' AND column_name = 'client_request_id') THEN
        ALTER TABLE request_logs ADD COLUMN client_request_id TEXT;
    END IF;
END $$;