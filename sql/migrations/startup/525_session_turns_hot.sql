-- Migration 525: independent session_turns hot table and atomic promotion.
-- The hot table contains the complete physical column set introduced by
-- migrations 430, 431, 456, 464, 513, and 524.

BEGIN;

ALTER TABLE public.session_turns
    DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;
ALTER TABLE public.session_turns
    ADD CONSTRAINT session_turns_submit_mode_check
        CHECK (submit_mode IN (
            'full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'
        ));

CREATE TABLE IF NOT EXISTS public.session_turns_hot (
    id BIGINT NOT NULL DEFAULT nextval('public.session_turns_id_seq'::regclass),
    session_id TEXT NOT NULL,
    turn_no INTEGER NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    request_id TEXT NOT NULL,
    project_id TEXT,
    namespace TEXT,
    parent_request_id TEXT,
    task_type TEXT,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submit_mode TEXT NOT NULL DEFAULT 'full',
    compression_applied BOOLEAN DEFAULT FALSE,
    compression_strategy TEXT,
    compression_meta JSONB DEFAULT '{}'::JSONB,
    compression_tokens_saved INTEGER,
    injection_verdict TEXT DEFAULT 'skip',
    output_verdict TEXT DEFAULT 'skip',
    model TEXT,
    provider TEXT,
    credential_id TEXT,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_write_tokens INTEGER,
    cost_usd NUMERIC(12,6),
    latency_ms INTEGER,
    status_code INTEGER,
    success BOOLEAN,
    error_kind TEXT,
    source_kind TEXT NOT NULL DEFAULT 'live',
    quality TEXT NOT NULL DEFAULT 'verified',
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    attachment_count INTEGER DEFAULT 0,
    attachment_total_bytes BIGINT DEFAULT 0,
    multimodal_types TEXT[] DEFAULT '{}',
    attempt_no INTEGER NOT NULL DEFAULT 0,
    tools JSONB NOT NULL DEFAULT '[]'::JSONB,
    title TEXT,
    summary TEXT,
    aggregate_applied_at TIMESTAMPTZ,
    t0_arrived_at TIMESTAMPTZ,
    t1_total_enqueued_at TIMESTAMPTZ,
    t2_total_dequeued_at TIMESTAMPTZ,
    t3_model_enqueued_at TIMESTAMPTZ,
    t4_model_dequeued_at TIMESTAMPTZ,
    t5_cred_enqueued_at TIMESTAMPTZ,
    t6_cred_dequeued_at TIMESTAMPTZ,
    t7_forward_start_at TIMESTAMPTZ,
    t8_response_start_at TIMESTAMPTZ,
    t9_response_end_at TIMESTAMPTZ,
    CONSTRAINT session_turns_hot_pkey PRIMARY KEY (id, partition_date),
    CONSTRAINT session_turns_hot_tenant_session_turn_key
        UNIQUE (tenant_id, session_id, turn_no, partition_date),
    CONSTRAINT session_turns_hot_tenant_request_key
        UNIQUE (tenant_id, request_id, partition_date),
    CONSTRAINT session_turns_hot_submit_mode_check
        CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only')),
    CONSTRAINT session_turns_hot_injection_verdict_check
        CHECK (injection_verdict IN ('pass', 'warn', 'block', 'skip')),
    CONSTRAINT session_turns_hot_output_verdict_check
        CHECK (output_verdict IN ('pass', 'warn', 'block', 'skip')),
    CONSTRAINT session_turns_hot_source_kind_check
        CHECK (source_kind IN ('live', 'backfill')),
    CONSTRAINT session_turns_hot_quality_check
        CHECK (quality IN ('verified', 'inferred', 'partial', 'rejected')),
    CONSTRAINT session_turns_hot_attachment_count_check
        CHECK (attachment_count IS NULL OR attachment_count >= 0),
    CONSTRAINT session_turns_hot_attachment_total_bytes_check
        CHECK (attachment_total_bytes IS NULL OR attachment_total_bytes >= 0),
    CONSTRAINT session_turns_hot_attempt_no_check
        CHECK (attempt_no >= 0)
) WITH (fillfactor = 90);

COMMENT ON TABLE public.session_turns_hot IS
    'Independent heap write table for recent session turns. Cold rows are atomically moved to public.session_turns by promote_session_turns_hot_to_partition().';

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_ts
    ON public.session_turns_hot (ts, id, partition_date);

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_request
    ON public.session_turns_hot (request_id);

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_ts
    ON public.session_turns_hot (tenant_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_session_turn
    ON public.session_turns_hot (tenant_id, session_id, turn_no DESC);

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_project
    ON public.session_turns_hot (tenant_id, project_id, ts DESC)
    WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_namespace
    ON public.session_turns_hot (tenant_id, namespace, ts DESC)
    WHERE namespace IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_parent_request
    ON public.session_turns_hot (tenant_id, parent_request_id, ts DESC)
    WHERE parent_request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_task_type
    ON public.session_turns_hot (tenant_id, task_type, ts DESC)
    WHERE task_type IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_tenant_t0_arrived
    ON public.session_turns_hot (tenant_id, t0_arrived_at DESC)
    WHERE t0_arrived_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_attachment_count
    ON public.session_turns_hot (tenant_id, attachment_count)
    WHERE attachment_count > 0;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_multimodal_types
    ON public.session_turns_hot USING GIN (multimodal_types)
    WHERE multimodal_types <> '{}';

ALTER TABLE public.session_turns_hot ENABLE ROW LEVEL SECURITY;

-- Keep the parent and hot owner contracts identical. The historical Migration
-- 457 policy only inspected request_logs, so a turn promoted before its request
-- log became invisible while the request log was still hot.
DROP POLICY IF EXISTS session_turns_owner_filter ON public.session_turns;
CREATE POLICY session_turns_owner_filter ON public.session_turns
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM (
                    SELECT gw_session_id, owner_user, ts
                    FROM public.request_logs_hot
                    WHERE gw_session_id IS NOT NULL
                    UNION ALL
                    SELECT gw_session_id, owner_user, ts
                    FROM public.request_logs
                    WHERE gw_session_id IS NOT NULL
                ) request_sources
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = public.session_turns.session_id
              AND first_rl.owner_user = current_setting('app.current_user', true)
        )
    );

DROP POLICY IF EXISTS session_turns_hot_tenant_isolation ON public.session_turns_hot;
CREATE POLICY session_turns_hot_tenant_isolation ON public.session_turns_hot
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

DROP POLICY IF EXISTS session_turns_hot_super_admin_bypass ON public.session_turns_hot;
CREATE POLICY session_turns_hot_super_admin_bypass ON public.session_turns_hot
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

DROP POLICY IF EXISTS session_turns_hot_owner_filter ON public.session_turns_hot;
CREATE POLICY session_turns_hot_owner_filter ON public.session_turns_hot
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM (
                    SELECT gw_session_id, owner_user, ts
                    FROM public.request_logs_hot
                    WHERE gw_session_id IS NOT NULL
                    UNION ALL
                    SELECT gw_session_id, owner_user, ts
                    FROM public.request_logs
                    WHERE gw_session_id IS NOT NULL
                ) request_sources
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = public.session_turns_hot.session_id
              AND first_rl.owner_user = current_setting('app.current_user', true)
        )
    );

DO $$
BEGIN
    IF current_setting('server_version_num')::integer < 150000 THEN
        RAISE EXCEPTION 'Migration 525 requires PostgreSQL 15 or newer';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_partitioned_table
        WHERE partrelid = 'public.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'public.session_turns must be a partitioned table';
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
        RAISE EXCEPTION 'session_turns_hot columns do not match public.session_turns';
    END IF;
END $$;

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
    SELECT 1
    FROM public.session_turns archived
    WHERE archived.tenant_id = hot.tenant_id
      AND archived.request_id = hot.request_id
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

COMMENT ON VIEW public.session_turns_with_current_month IS
    'Explicit-column union of recent session_turns_hot rows and all attached public.session_turns partitions.';

CREATE OR REPLACE FUNCTION public.session_turns_advisory_lock_key(
    p_tenant_id TEXT,
    p_session_id TEXT
)
RETURNS BIGINT
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT hashtextextended(p_tenant_id || ':' || p_session_id, 0)
$$;

COMMENT ON FUNCTION public.session_turns_advisory_lock_key(TEXT, TEXT) IS
    'Canonical tenant/session advisory-lock key shared by session turn writers, enrichment, aggregation, and hot-row promotion.';

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

DO $$
DECLARE
    v_constraint TEXT;
    v_index TEXT;
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE oid = 'public.session_turns_hot'::regclass
          AND relkind = 'r'
          AND relrowsecurity
    ) OR EXISTS (
        SELECT 1 FROM pg_inherits
        WHERE inhrelid = 'public.session_turns_hot'::regclass
           OR inhparent = 'public.session_turns_hot'::regclass
    ) THEN
        RAISE EXCEPTION 'public.session_turns_hot must be an independent RLS-enabled heap table';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.session_turns'::regclass
          AND conname = 'session_turns_submit_mode_check'
          AND convalidated
          AND pg_get_constraintdef(oid) ~ 'attachment_only'
    ) THEN
        RAISE EXCEPTION 'parent submit_mode constraint must allow attachment_only';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_attrdef d
        JOIN pg_attribute a
          ON a.attrelid = d.adrelid AND a.attnum = d.adnum
        WHERE d.adrelid = 'public.session_turns_hot'::regclass
          AND a.attname = 'id'
          AND pg_get_expr(d.adbin, d.adrelid) LIKE 'nextval(%session_turns_id_seq%'
    ) THEN
        RAISE EXCEPTION 'session_turns_hot.id must reuse public.session_turns_id_seq';
    END IF;

    FOREACH v_constraint IN ARRAY ARRAY[
        'session_turns_hot_pkey',
        'session_turns_hot_tenant_session_turn_key',
        'session_turns_hot_tenant_request_key',
        'session_turns_hot_submit_mode_check',
        'session_turns_hot_injection_verdict_check',
        'session_turns_hot_output_verdict_check',
        'session_turns_hot_source_kind_check',
        'session_turns_hot_quality_check',
        'session_turns_hot_attachment_count_check',
        'session_turns_hot_attachment_total_bytes_check',
        'session_turns_hot_attempt_no_check'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conrelid = 'public.session_turns_hot'::regclass
              AND conname = v_constraint
              AND convalidated
        ) THEN
            RAISE EXCEPTION 'required hot constraint % is missing or invalid', v_constraint;
        END IF;
    END LOOP;

    FOREACH v_index IN ARRAY ARRAY[
        'idx_session_turns_hot_ts',
        'idx_session_turns_hot_request',
        'idx_session_turns_hot_tenant_ts',
        'idx_session_turns_hot_tenant_session_turn',
        'idx_session_turns_hot_tenant_project',
        'idx_session_turns_hot_tenant_namespace',
        'idx_session_turns_hot_tenant_parent_request',
        'idx_session_turns_hot_tenant_task_type',
        'idx_session_turns_hot_tenant_t0_arrived',
        'idx_session_turns_hot_attachment_count',
        'idx_session_turns_hot_multimodal_types'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1
            FROM pg_class i
            JOIN pg_index x ON x.indexrelid = i.oid
            WHERE i.relnamespace = 'public'::regnamespace
              AND i.relname = v_index
              AND x.indrelid = 'public.session_turns_hot'::regclass
              AND x.indisvalid
        ) THEN
            RAISE EXCEPTION 'required hot index public.% is missing or invalid', v_index;
        END IF;
    END LOOP;

    IF (SELECT count(*) FROM pg_policies
        WHERE schemaname = 'public' AND tablename = 'session_turns_hot') <> 3 THEN
        RAISE EXCEPTION 'public.session_turns_hot must have exactly three RLS policies';
    END IF;

    IF to_regclass('public.session_turns_with_current_month') IS NULL
       OR to_regprocedure(
           'public.session_turns_advisory_lock_key(text,text)'
       ) IS NULL
       OR to_regprocedure(
           'public.promote_session_turns_hot_to_partition(interval,integer)'
       ) IS NULL THEN
        RAISE EXCEPTION 'session_turns hot view, lock-key function, or promote function is missing';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_class
        WHERE oid = 'public.session_turns_with_current_month'::regclass
          AND 'security_invoker=true' = ANY (reloptions)
    ) THEN
        RAISE EXCEPTION 'session_turns_with_current_month must use security_invoker=true';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (VALUES
            ('session_turns', 'session_turns_owner_filter'),
            ('session_turns_hot', 'session_turns_hot_owner_filter')
        ) expected(table_name, policy_name)
        WHERE NOT EXISTS (
            SELECT 1
            FROM pg_policies p
            WHERE p.schemaname = 'public'
              AND p.tablename = expected.table_name
              AND p.policyname = expected.policy_name
              AND p.permissive = 'RESTRICTIVE'
              AND p.qual::TEXT ~ 'request_logs_hot'
              AND p.qual::TEXT ~ 'request_logs([^_[:alnum:]]|$)'
        )
    ) THEN
        RAISE EXCEPTION 'parent and hot owner filters must be restrictive and inspect both request log stores';
    END IF;
END $$;

COMMIT;
