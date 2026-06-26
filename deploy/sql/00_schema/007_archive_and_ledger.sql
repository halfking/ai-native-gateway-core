-- ============================================================================
-- llm-gateway-go 归档表和Request WAL表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Request Logs Archive 分区表（columnar存储，历史数据）
-- 使用 PostgreSQL columnar 扩展进行列式存储，节省存储空间提升查询性能
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_logs_archive (
    id bigint NOT NULL,
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
    CONSTRAINT chk_archive_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_archive_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
) PARTITION BY RANGE (ts);

-- ----------------------------------------------------------------------------
-- Columnar 分区（使用 USING columnar）
-- ----------------------------------------------------------------------------
-- 创建 columnar 分区示例：
-- CREATE TABLE request_logs_archive_2026_06
--     PARTITION OF request_logs_archive
--     FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
--     USING columnar;
CREATE TABLE IF NOT EXISTS public.request_logs_archive_2026_06
    PARTITION OF public.request_logs_archive
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
    USING columnar;

CREATE TABLE IF NOT EXISTS public.request_logs_archive_2026_07
    PARTITION OF public.request_logs_archive
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')
    USING columnar;

-- ----------------------------------------------------------------------------
-- Request Logs Archive RLS
-- ----------------------------------------------------------------------------
ALTER TABLE public.request_logs_archive ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_request_logs_archive ON public.request_logs_archive;
CREATE POLICY tenant_isolation_request_logs_archive ON public.request_logs_archive
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- archive_request_logs 函数 - 将heap分区迁移到columnar分区
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.archive_request_logs(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    col_list    text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_logs_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_logs_archive', src_part;
    END IF;

    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;

    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

-- ----------------------------------------------------------------------------
-- ensure_next_month_archive_partition 函数 - 预创建下月的columnar分区
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition()
    RETURNS void
    LANGUAGE plpgsql
AS $func$
DECLARE
    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
    partition_name   text := 'request_logs_archive_' || to_char(next_month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, next_month_start, next_month_end
        );
    END IF;
END;
$func$;

-- ----------------------------------------------------------------------------
-- Request WAL 表（分区表）- 请求WAL日志
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_wal (
    id BIGSERIAL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT NOT NULL,
    api_key_id BIGINT,
    request_id TEXT NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    event_data JSONB,
    PRIMARY KEY (id, ts)
) PARTITION BY RANGE (ts);

CREATE TABLE IF NOT EXISTS public.request_wal_2026_06
    PARTITION OF public.request_wal
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS public.request_wal_2026_07
    PARTITION OF public.request_wal
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- ----------------------------------------------------------------------------
-- Request Envelope 表 - 请求信封
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_envelope (
    id SERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    api_key_id BIGINT,
    client_model TEXT NOT NULL,
    request_body JSONB NOT NULL,
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_request_envelope_tenant ON request_envelope(tenant_id);
CREATE INDEX IF NOT EXISTS idx_request_envelope_created ON request_envelope(created_at DESC);

-- ----------------------------------------------------------------------------
-- Request WAL Bodies 表 - 请求体存储
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.request_wal_bodies (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_body JSONB,
    response_body JSONB,
    metadata JSONB
);
CREATE INDEX IF NOT EXISTS idx_request_wal_bodies_request ON request_wal_bodies(request_id);
CREATE INDEX IF NOT EXISTS idx_request_wal_bodies_ts ON request_wal_bodies(ts DESC);