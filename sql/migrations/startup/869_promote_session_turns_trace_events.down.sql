-- 869_promote_session_turns_trace_events.down.sql
-- 回滚：把 promote_session_turns_hot_to_partition 恢复成 869 之前的**线上终态体**
--        ——即 640_session_turns_protocol_fields.sql 那份硬编码 54 列清单。
--
-- ⚠ 已知代价，回滚前必须知悉并取得 owner 拍板：
--   恢复回去就等于**重新打开本次确认的数据丢失缺陷**。硬编码 54 列清单相对
--   hot 的 106 列会静默丢弃 52 列（trace_events、is_final_success、
--   request_delta、response_delta、identity_hash、system_fingerprint、
--   upstream_*、stream_*、failure_*、routing_*、client_* 等）——每次晋级都丢，
--   不报错、moved 计数照常递增。
--
--   因此本 down 只应作为「869 本身出问题」的应急退路，不应作为常态操作。
--   若 869 的目录派生体在你的库上有别的兼容问题，请优先修 869 而不是走 down。
--
--   注：installer 全新安装通道的 869 之前终态体是 707（目录派生体，非 640）。
--   本 down 面向存量库/部署库（seq 通道，640 为终态）；在全新安装库上执行它
--   会把函数退到 640 的旧形态，属预期（那正是 869 之前 installer 也在修的分叉）。

BEGIN;

SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(
    p_retention INTERVAL DEFAULT '7 days',
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

    IF EXISTS (
        WITH parent_columns AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns'::regclass
              AND attnum > 0 AND NOT attisdropped
        ), hot_columns AS (
            SELECT attname, atttypid, atttypmod, attnotnull
            FROM pg_attribute
            WHERE attrelid = 'public.session_turns_hot'::regclass
              AND attnum > 0 AND NOT attisdropped
        )
        SELECT 1
        FROM parent_columns p
        FULL JOIN hot_columns h USING (attname)
        WHERE p.attname IS NULL OR h.attname IS NULL
           OR p.atttypid <> h.atttypid
           OR p.atttypmod <> h.atttypmod
           OR p.attnotnull <> h.attnotnull
    ) THEN
        RAISE EXCEPTION 'session_turns hot/parent column contract has drifted';
    END IF;

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

    WITH moved AS (
        DELETE FROM public.session_turns_hot h
        USING session_turns_promotion_batch b
        WHERE h.id = b.id
          AND h.partition_date = b.partition_date
        RETURNING
            h.id, h.session_id, h.turn_no, h.tenant_id, h.request_id,
            h.project_id, h.namespace, h.parent_request_id, h.task_type,
            h.ts, h.submit_mode, h.compression_applied,
            h.compression_strategy, h.compression_meta,
            h.compression_tokens_saved, h.injection_verdict,
            h.output_verdict, h.model, h.provider, h.credential_id,
            h.prompt_tokens, h.completion_tokens, h.cache_read_tokens,
            h.cache_write_tokens, h.cost_usd, h.latency_ms, h.status_code,
            h.success, h.error_kind, h.source_kind, h.quality,
            h.partition_date, h.attachment_count, h.attachment_total_bytes,
            h.multimodal_types, h.attempt_no, h.tools, h.title, h.summary, h.digest,
            h.aggregate_applied_at, h.t0_arrived_at,
            h.t1_total_enqueued_at, h.t2_total_dequeued_at,
            h.t3_model_enqueued_at, h.t4_model_dequeued_at,
            h.t5_cred_enqueued_at, h.t6_cred_dequeued_at,
            h.t7_forward_start_at, h.t8_response_start_at,
            h.t9_response_end_at,
            h.client_protocol, h.upstream_protocol, h.ir_metadata
    ), inserted AS (
        INSERT INTO public.session_turns (
            id, session_id, turn_no, tenant_id, request_id,
            project_id, namespace, parent_request_id, task_type,
            ts, submit_mode, compression_applied, compression_strategy,
            compression_meta, compression_tokens_saved, injection_verdict,
            output_verdict, model, provider, credential_id, prompt_tokens,
            completion_tokens, cache_read_tokens, cache_write_tokens,
            cost_usd, latency_ms, status_code, success, error_kind,
            source_kind, quality, partition_date, attachment_count,
            attachment_total_bytes, multimodal_types, attempt_no, tools,
            title, summary, digest, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at,
            client_protocol, upstream_protocol, ir_metadata
        )
        SELECT
            id, session_id, turn_no, tenant_id, request_id,
            project_id, namespace, parent_request_id, task_type,
            ts, submit_mode, compression_applied, compression_strategy,
            compression_meta, compression_tokens_saved, injection_verdict,
            output_verdict, model, provider, credential_id, prompt_tokens,
            completion_tokens, cache_read_tokens, cache_write_tokens,
            cost_usd, latency_ms, status_code, success, error_kind,
            source_kind, quality, partition_date, attachment_count,
            attachment_total_bytes, multimodal_types, attempt_no, tools,
            title, summary, digest, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at,
            client_protocol, upstream_protocol, ir_metadata
        FROM moved
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted;

    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION public.promote_session_turns_hot_to_partition(INTERVAL, INTEGER) IS
  'Move cold rows from session_turns_hot to monthly partitions (869 rollback: restored to the pre-869 hardcoded 54-column body from 640). WARNING: this body silently drops 52 columns on promotion, trace_events included.';

-- 双账本回退：登记 869 已回滚。
DELETE FROM public.schema_migrations WHERE version = '869';

COMMIT;
