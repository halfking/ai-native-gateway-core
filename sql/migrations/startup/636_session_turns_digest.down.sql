-- Migration 636 down: remove nullable digest JSONB and restore 526 projections.

BEGIN;

CREATE TEMP TABLE session_turns_current_month_grants ON COMMIT DROP AS
SELECT grantee, privilege_type
FROM information_schema.role_table_grants
WHERE table_schema = 'public'
  AND table_name = 'session_turns_with_current_month'
  AND privilege_type = 'SELECT';

DROP VIEW IF EXISTS public.session_turns_with_current_month;
DROP FUNCTION IF EXISTS public.promote_session_turns_hot_to_partition(INTERVAL, INTEGER);

ALTER TABLE public.session_turns_hot DROP COLUMN IF EXISTS digest;
ALTER TABLE public.session_turns DROP COLUMN IF EXISTS digest;

CREATE OR REPLACE VIEW public.session_turns_with_current_month
WITH (security_invoker = true) AS
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at
FROM public.session_turns_hot hot
WHERE NOT EXISTS (
    SELECT 1 FROM public.session_turns archived
    WHERE archived.tenant_id = hot.tenant_id AND archived.request_id = hot.request_id
)
UNION ALL
SELECT
    id, session_id, turn_no, tenant_id, request_id,
    project_id, namespace, parent_request_id, task_type,
    ts, submit_mode, compression_applied, compression_strategy,
    compression_meta, compression_tokens_saved, injection_verdict,
    output_verdict, model, provider, credential_id, prompt_tokens,
    completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind, source_kind, quality,
    partition_date, attachment_count, attachment_total_bytes,
    multimodal_types, attempt_no, tools, title, summary,
    aggregate_applied_at, t0_arrived_at, t1_total_enqueued_at,
    t2_total_dequeued_at, t3_model_enqueued_at, t4_model_dequeued_at,
    t5_cred_enqueued_at, t6_cred_dequeued_at, t7_forward_start_at,
    t8_response_start_at, t9_response_end_at
FROM public.session_turns;

DO $$
DECLARE
    grant_row RECORD;
BEGIN
    FOR grant_row IN SELECT grantee FROM session_turns_current_month_grants LOOP
        EXECUTE format('GRANT SELECT ON public.session_turns_with_current_month TO %I', grant_row.grantee);
    END LOOP;
END $$;

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

    -- The writer, aggregate/enrichment paths, and promotion all take this same
    -- tenant/session lock. Stable ordering prevents deadlocks for mixed batches.
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
            h.multimodal_types, h.attempt_no, h.tools, h.title, h.summary,
            h.aggregate_applied_at, h.t0_arrived_at,
            h.t1_total_enqueued_at, h.t2_total_dequeued_at,
            h.t3_model_enqueued_at, h.t4_model_dequeued_at,
            h.t5_cred_enqueued_at, h.t6_cred_dequeued_at,
            h.t7_forward_start_at, h.t8_response_start_at,
            h.t9_response_end_at
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
            title, summary, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at
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
            title, summary, aggregate_applied_at, t0_arrived_at,
            t1_total_enqueued_at, t2_total_dequeued_at,
            t3_model_enqueued_at, t4_model_dequeued_at,
            t5_cred_enqueued_at, t6_cred_dequeued_at,
            t7_forward_start_at, t8_response_start_at, t9_response_end_at
        FROM moved
        RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM inserted;

    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION public.promote_session_turns_hot_to_partition(INTERVAL, INTEGER) IS
    'Atomically moves one validated cold batch from session_turns_hot into attached monthly partitions using DELETE RETURNING plus INSERT, without ON CONFLICT.';

COMMIT;
