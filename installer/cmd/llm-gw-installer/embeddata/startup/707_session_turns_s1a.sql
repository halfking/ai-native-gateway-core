-- Migration 707: 存储优化方案 v2 · S1a —— session_turns 宽表化（D1 五类补采 + 正文 + is_final_success）
--
-- docs/03-design/04-data-design/storage-optimization-plan.md §3 D1/§4 S1a：
-- session_turns 成为 turn 级唯一事实源（宽表路线），补采 request_logs 独有的
-- 五类数据，使弃用 request_logs 后（S2 视图拼装/S4 停写）数据不丢：
--   计费组（credits_charged 是计费事实源，D7 双读校验的前提）
--   路由组 / 诊断组 / 检索·完整性组
--   正文组 request_delta/response_delta（S1b 起 writer 按
--     storage.session_turns_bodies_enabled 灰度写入）
--   is_final_success + 部分唯一索引（D8 claim 语义的落点；Go 侧 claim 改写
--     随 S3 timeline 切换进行，本迁移只立 schema）
--
-- 数据源事实（2026-09-14 审计）：telemetry.RequestLogEntry 缺 TraceEvents /
-- SearchText / RequestChecksum / RawModelName / StreamDoneSent（最接近的是
-- StreamDoneReceived）——前四列本迁移照建、暂为 NULL（S2 视图 NULL 补位登记，
-- 方案 §9 风险表），stream_done_sent 映射 StreamDoneReceived。
--
-- promote 联动：688 体 promote_session_turns_hot_to_partition 用显式列清单
-- 搬行，加列后新列会在 promote 时静默丢成 NULL —— 本迁移 CREATE OR REPLACE
-- 该函数为「列集合契约校验 + 目录派生列清单」形态：契约校验保证父表与 hot
-- 的（列名:类型:非空）集合全等（历史库两者列序可能漂移，故按集合比较），
-- 搬行 INSERT 按列名映射、清单取自目录，后续任何对称加列自动流经 promote，
-- 不再需要逐列维护（与 688 同样的批选择、advisory lock、ensure partition、
-- 防重语义；默认值对齐 688 的 8h/5000）。
-- 本文件在通道 files 清单内是该函数唯一定义者，无 chain 登记需求。
--
-- Compatibility: 纯可空加列 + hot 同步加列（维持 688 契约校验），零回填。
-- down: DROP 补列（目录派生列清单的 promote 体对少列父表同样成立，down
-- 无需回滚函数）。

BEGIN;

-- =============================================
-- 1. session_turns 父表补列（与 hot 完全同形）
-- =============================================

ALTER TABLE public.session_turns
    -- 正文组（S1b：writer 按 storage.session_turns_bodies_enabled 写入）
    ADD COLUMN IF NOT EXISTS request_delta JSONB,
    ADD COLUMN IF NOT EXISTS response_delta JSONB,
    -- D8 claim
    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN,
    -- 计费组
    ADD COLUMN IF NOT EXISTS api_key_id TEXT,
    ADD COLUMN IF NOT EXISTS application_id TEXT,
    ADD COLUMN IF NOT EXISTS end_user_id TEXT,
    ADD COLUMN IF NOT EXISTS customer_id BIGINT,
    ADD COLUMN IF NOT EXISTS credits_charged BIGINT,
    ADD COLUMN IF NOT EXISTS cost_display DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS cost_currency TEXT,
    ADD COLUMN IF NOT EXISTS work_type TEXT,
    ADD COLUMN IF NOT EXISTS token_band TEXT,
    ADD COLUMN IF NOT EXISTS usage_source TEXT,
    -- 路由组
    ADD COLUMN IF NOT EXISTS is_auto_request BOOLEAN,
    ADD COLUMN IF NOT EXISTS auto_decision TEXT,
    ADD COLUMN IF NOT EXISTS auto_confidence DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS task_type_chosen TEXT,
    ADD COLUMN IF NOT EXISTS routing_attempts JSONB,
    ADD COLUMN IF NOT EXISTS routing_summary TEXT,
    ADD COLUMN IF NOT EXISTS canonical_id BIGINT,
    ADD COLUMN IF NOT EXISTS canonical_model TEXT,
    ADD COLUMN IF NOT EXISTS raw_model_name TEXT,
    -- 诊断组
    ADD COLUMN IF NOT EXISTS trace_events JSONB,
    ADD COLUMN IF NOT EXISTS failure_stage TEXT,
    ADD COLUMN IF NOT EXISTS failure_detail_code TEXT,
    ADD COLUMN IF NOT EXISTS upstream_status_code INT,
    ADD COLUMN IF NOT EXISTS upstream_finish_reason TEXT,
    ADD COLUMN IF NOT EXISTS stream_first_chunk_ms INT,
    ADD COLUMN IF NOT EXISTS stream_chunk_count INT,
    ADD COLUMN IF NOT EXISTS stream_interrupted BOOLEAN,
    ADD COLUMN IF NOT EXISTS stream_done_sent BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_request_id TEXT,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS egress_protocol TEXT,
    -- 检索/完整性组
    ADD COLUMN IF NOT EXISTS search_text TEXT,
    ADD COLUMN IF NOT EXISTS request_preview TEXT,
    ADD COLUMN IF NOT EXISTS response_preview TEXT,
    ADD COLUMN IF NOT EXISTS transform_summary TEXT,
    ADD COLUMN IF NOT EXISTS identity_hash TEXT,
    ADD COLUMN IF NOT EXISTS request_checksum TEXT,
    ADD COLUMN IF NOT EXISTS response_checksum TEXT,
    ADD COLUMN IF NOT EXISTS system_fingerprint TEXT,
    ADD COLUMN IF NOT EXISTS origin_stage TEXT,
    ADD COLUMN IF NOT EXISTS origin_actor TEXT,
    ADD COLUMN IF NOT EXISTS client_ip TEXT,
    ADD COLUMN IF NOT EXISTS client_forwarded_for TEXT,
    ADD COLUMN IF NOT EXISTS agent_name TEXT,
    ADD COLUMN IF NOT EXISTS agent_type TEXT,
    ADD COLUMN IF NOT EXISTS virtual_client_id TEXT;

-- =============================================
-- 2. session_turns_hot 同形补列（688 契约校验要求集合+类型全等）
-- =============================================

ALTER TABLE public.session_turns_hot
    ADD COLUMN IF NOT EXISTS request_delta JSONB,
    ADD COLUMN IF NOT EXISTS response_delta JSONB,
    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN,
    ADD COLUMN IF NOT EXISTS api_key_id TEXT,
    ADD COLUMN IF NOT EXISTS application_id TEXT,
    ADD COLUMN IF NOT EXISTS end_user_id TEXT,
    ADD COLUMN IF NOT EXISTS customer_id BIGINT,
    ADD COLUMN IF NOT EXISTS credits_charged BIGINT,
    ADD COLUMN IF NOT EXISTS cost_display DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS cost_currency TEXT,
    ADD COLUMN IF NOT EXISTS work_type TEXT,
    ADD COLUMN IF NOT EXISTS token_band TEXT,
    ADD COLUMN IF NOT EXISTS usage_source TEXT,
    ADD COLUMN IF NOT EXISTS is_auto_request BOOLEAN,
    ADD COLUMN IF NOT EXISTS auto_decision TEXT,
    ADD COLUMN IF NOT EXISTS auto_confidence DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS task_type_chosen TEXT,
    ADD COLUMN IF NOT EXISTS routing_attempts JSONB,
    ADD COLUMN IF NOT EXISTS routing_summary TEXT,
    ADD COLUMN IF NOT EXISTS canonical_id BIGINT,
    ADD COLUMN IF NOT EXISTS canonical_model TEXT,
    ADD COLUMN IF NOT EXISTS raw_model_name TEXT,
    ADD COLUMN IF NOT EXISTS trace_events JSONB,
    ADD COLUMN IF NOT EXISTS failure_stage TEXT,
    ADD COLUMN IF NOT EXISTS failure_detail_code TEXT,
    ADD COLUMN IF NOT EXISTS upstream_status_code INT,
    ADD COLUMN IF NOT EXISTS upstream_finish_reason TEXT,
    ADD COLUMN IF NOT EXISTS stream_first_chunk_ms INT,
    ADD COLUMN IF NOT EXISTS stream_chunk_count INT,
    ADD COLUMN IF NOT EXISTS stream_interrupted BOOLEAN,
    ADD COLUMN IF NOT EXISTS stream_done_sent BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_request_id TEXT,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS egress_protocol TEXT,
    ADD COLUMN IF NOT EXISTS search_text TEXT,
    ADD COLUMN IF NOT EXISTS request_preview TEXT,
    ADD COLUMN IF NOT EXISTS response_preview TEXT,
    ADD COLUMN IF NOT EXISTS transform_summary TEXT,
    ADD COLUMN IF NOT EXISTS identity_hash TEXT,
    ADD COLUMN IF NOT EXISTS request_checksum TEXT,
    ADD COLUMN IF NOT EXISTS response_checksum TEXT,
    ADD COLUMN IF NOT EXISTS system_fingerprint TEXT,
    ADD COLUMN IF NOT EXISTS origin_stage TEXT,
    ADD COLUMN IF NOT EXISTS origin_actor TEXT,
    ADD COLUMN IF NOT EXISTS client_ip TEXT,
    ADD COLUMN IF NOT EXISTS client_forwarded_for TEXT,
    ADD COLUMN IF NOT EXISTS agent_name TEXT,
    ADD COLUMN IF NOT EXISTS agent_type TEXT,
    ADD COLUMN IF NOT EXISTS virtual_client_id TEXT;

-- =============================================
-- 3. is_final_success 部分唯一索引（D8：每会话至多一条唯一成功）
-- =============================================

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_turns_final_success
    ON public.session_turns (tenant_id, session_id, partition_date)
    WHERE is_final_success;

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_turns_hot_final_success
    ON public.session_turns_hot (tenant_id, session_id, partition_date)
    WHERE is_final_success;

-- =============================================
-- 4. promote_session_turns_hot_to_partition 重写
--    （有序契约校验 + SELECT *；语义承 688）
-- =============================================

CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(
    p_retention INTERVAL DEFAULT '8 hours',
    p_batch_size INTEGER DEFAULT 5000
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved BIGINT := 0;
    v_conflicts BIGINT := 0;
    v_partition_date DATE;
    v_partition REGCLASS;
    v_session RECORD;
    v_request RECORD;
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
BEGIN
    IF p_retention IS NULL OR p_retention <= INTERVAL '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be a positive interval';
    END IF;

    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
    END IF;

    IF to_regclass('public.session_turns_hot') IS NULL
       OR to_regclass('public.session_turns') IS NULL THEN
        RAISE EXCEPTION 'session_turns hot and parent tables must both exist';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_partitioned_table
        WHERE partrelid = 'public.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'public.session_turns must remain partitioned';
    END IF;

    -- 707 强化的列契约：父表与 hot 的（列名:类型:非空）集合必须全等。
    -- 历史库中父表/hot 的列序可能不同（各迁移独立追加），故契约按集合
    -- 比较；promote 的列映射用目录派生的显式列名清单（见下），对列序不
    -- 敏感。任何一侧漏加列/类型漂移在此刻直接报错，绝不静默丢列。
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION 'session_turns hot/parent column contract has drifted (column set mismatch)';
    END IF;

    -- 显式列名清单：取自 hot 的目录（attnum 序）。707 的 50 个新列与后续
    -- 任何对称加列自动流经 promote，无需再改本函数；列序漂移不影响正确
    -- 性（INSERT 按名映射）。
    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
      INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;

    PERFORM pg_advisory_xact_lock(
        hashtextextended('public.promote_session_turns_hot_to_partition', 0)
    );

    CREATE TEMP TABLE IF NOT EXISTS session_turns_promotion_batch (
        id BIGINT NOT NULL,
        partition_date DATE NOT NULL,
        tenant_id TEXT NOT NULL,
        session_id TEXT NOT NULL,
        request_id TEXT NOT NULL,
        PRIMARY KEY (id, partition_date)
    ) ON COMMIT DROP;
    TRUNCATE session_turns_promotion_batch;

    SELECT count(*) INTO v_conflicts
    FROM (
        SELECT 1
        FROM public.session_turns_hot h
        WHERE h.ts < statement_timestamp() - p_retention
          AND EXISTS (
              SELECT 1
              FROM public.session_turns archived
              WHERE archived.tenant_id = h.tenant_id
                AND archived.request_id = h.request_id
          )
        LIMIT p_batch_size
    ) conflicts;
    IF v_conflicts > 0 THEN
        RAISE WARNING 'session_turns promote skipped % duplicate hot rows already present in partitions',
            v_conflicts;
    END IF;

    INSERT INTO session_turns_promotion_batch (
        id, partition_date, tenant_id, session_id, request_id
    )
    SELECT h.id, h.partition_date, h.tenant_id, h.session_id, h.request_id
    FROM public.session_turns_hot h
    WHERE h.ts < statement_timestamp() - p_retention
      AND NOT EXISTS (
          SELECT 1
          FROM public.session_turns archived
          WHERE archived.tenant_id = h.tenant_id
            AND archived.request_id = h.request_id
      )
    ORDER BY h.ts, h.id, h.partition_date
    LIMIT p_batch_size;

    FOR v_session IN
        SELECT tenant_id, session_id
        FROM session_turns_promotion_batch
        GROUP BY tenant_id, session_id
        ORDER BY tenant_id, session_id
    LOOP
        PERFORM pg_advisory_xact_lock(
            public.session_turns_advisory_lock_key(
                v_session.tenant_id,
                v_session.session_id
            )
        );
    END LOOP;

    FOR v_request IN
        SELECT tenant_id, request_id
        FROM session_turns_promotion_batch
        GROUP BY tenant_id, request_id
        ORDER BY tenant_id, request_id
    LOOP
        PERFORM pg_advisory_xact_lock(
            public.session_turns_advisory_lock_key(
                v_request.tenant_id,
                'request:' || v_request.request_id
            )
        );
    END LOOP;

    FOR v_partition_date IN
        SELECT DISTINCT partition_date
        FROM session_turns_promotion_batch
    LOOP
        PERFORM public.ensure_sessions_v2_partitions(v_partition_date);
        v_partition := to_regclass(
            format('public.session_turns_%s', to_char(v_partition_date, 'YYYY_MM'))
        );

        IF v_partition IS NULL OR NOT EXISTS (
            SELECT 1
            FROM pg_inherits
            WHERE inhparent = 'public.session_turns'::regclass
              AND inhrelid = v_partition
        ) THEN
            RAISE EXCEPTION 'no attached session_turns partition for partition_date %',
                v_partition_date;
        END IF;
    END LOOP;

    -- 707：目录派生列清单 + 按名映射。契约校验已保证父表与 hot 列集合
    -- 全等；后续任何对称加列自动流经 promote，无需再改本函数。
    EXECUTE format(
        'WITH moved AS (
            DELETE FROM public.session_turns_hot h
            USING session_turns_promotion_batch b
            WHERE h.id = b.id
              AND h.partition_date = b.partition_date
            RETURNING h.*
        ), inserted AS (
            INSERT INTO public.session_turns (%s)
            SELECT %s FROM moved
            RETURNING 1
        )
        SELECT count(*) FROM inserted', v_cols, v_cols)
    INTO v_moved;

    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION public.promote_session_turns_hot_to_partition(INTERVAL, INTEGER) IS
    'Move cold rows from session_turns_hot to monthly partitions (707 rewrite:
     set-equality column-contract check + catalog-derived explicit column list
     so symmetric ADD COLUMN flows through promotion without editing this
     function; order-insensitive for legacy parent/hot column-order drift).';

-- =============================================
-- 5. 契约自检：本迁移执行后 promote 必须能通过有序契约
-- =============================================

DO $$
DECLARE
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
BEGIN
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION '707 post-condition failed: session_turns hot/parent column sets diverge';
    END IF;
    IF to_regclass('public.session_turns') IS NULL
       OR NOT EXISTS (
           SELECT 1 FROM pg_indexes
           WHERE schemaname='public' AND tablename='session_turns'
             AND indexname='uq_session_turns_final_success'
       ) THEN
        RAISE EXCEPTION '707 post-condition failed: uq_session_turns_final_success missing';
    END IF;
END
$$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('707', 'storage plan v2 S1a: session_turns wide-table backfill columns + is_final_success + promote SELECT-* rewrite')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
