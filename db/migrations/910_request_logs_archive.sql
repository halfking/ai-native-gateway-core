-- ============================================================================
-- Migration 910: request_logs_archive (columnar tiered storage)
-- ============================================================================
-- 目标：实现 request_logs heap 主表 + columnar 分区归档表的双层架构
-- 原则：当月数据 heap（高频写入 + UPDATE/DELETE/ON CONFLICT），
--       历史月份 columnar（只读 + 压缩节省空间）
-- 数据流：每月调用 archive_request_logs('YYYY-MM-01') 将主表上月数据迁移到
--        request_logs_archive_<YYYY_MM>（columnar 子分区），然后 DETACH + DROP 主表分区
-- 父表：request_logs_archive (heap, RANGE (ts))
-- 子分区：request_logs_archive_2026_07/08/... (USING columnar)
-- 注意：columnar 表不支持 UPDATE/DELETE/ON CONFLICT/UNIQUE INDEX，所以历史数据
--       一旦迁移就只读（与业务语义一致：历史不会变）
-- ============================================================================

BEGIN;

-- 1. 创建归档父表（heap，RANGE 分区，兼容主表结构）
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
    client_request_id text,  -- 2026-06-27: added per 5373d963 hotfix
    CONSTRAINT chk_archive_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_archive_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
) PARTITION BY RANGE (ts);

-- 2. Comment 父表
COMMENT ON TABLE public.request_logs_archive IS
'Tiered storage: columnar partitions for historical request_logs. Monthly partitions use Citus columnar (compressed, read-only). Data flow: monthly archive_request_logs(archive_month) migrates request_logs_YYYY_MM (heap) into request_logs_archive_YYYY_MM (columnar) and drops the source partition. Use UNION ALL across request_logs + request_logs_archive for time-range queries.';

-- 3. RLS policy: 复用主表策略 (tenant isolation)
ALTER TABLE public.request_logs_archive ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_request_logs_archive ON public.request_logs_archive;
CREATE POLICY tenant_isolation_request_logs_archive ON public.request_logs_archive
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- 4. 创建 archive_request_logs 函数 (列感知版)
CREATE OR REPLACE FUNCTION public.archive_request_logs(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    partition_existed boolean := false;
    col_list    text;
BEGIN
    -- 1. 检查源分区是否存在
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    partition_existed := true;

    -- 2. 创建目标 columnar 分区（如果不存在）
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
        RAISE NOTICE 'Created archive columnar partition: %', dst_part;
    END IF;

    -- 3. 构建列交集 (源和目标都有的列)
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

    -- 4. INSERT columnar 用显式列列表 (columnar 不会因为列序不同失败)
    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;
    RAISE NOTICE 'Migrated % rows from % to % (columnar, column-aware)', row_count, src_part, dst_part;

    -- 5. DETACH + DROP 源分区（释放 heap 空间）
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);
    RAISE NOTICE 'Dropped source heap partition: %', src_part;

    RETURN QUERY SELECT 'success'::text, row_count, partition_existed;
END;
$$;

COMMENT ON FUNCTION public.archive_request_logs(archive_month date) IS
'Archive one month of request_logs into request_logs_archive (columnar). Column-aware: uses explicit column list, robust against column-order differences between source and target partitions. Drops the source heap partition after migration (releases space). Idempotent: skips if source partition does not exist.';

-- 5. Helper: 创建下个月 columnar 分区（用于下个月初的预创建）
CREATE OR REPLACE FUNCTION public.ensure_next_month_archive_partition()
    RETURNS void
    LANGUAGE plpgsql
    AS $$
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
        RAISE NOTICE 'Pre-created next month columnar partition: %', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION public.ensure_next_month_archive_partition() IS
'Pre-create the next month columnar partition for request_logs_archive. Call this from a cron job or background service at month-end so the columnar partition is ready when archive_request_logs() is called.';

-- 6. Helper view: 跨主表 + 归档的合并查询（暂跳过因为有列类型顺序问题待研究）
-- CREATE OR REPLACE VIEW public.request_logs_all AS
--     SELECT * FROM public.request_logs
--     UNION ALL
--     SELECT * FROM public.request_logs_archive;
-- (View 暂时禁用，请用 SQL 显式 UNION ALL 查询)

COMMIT;
