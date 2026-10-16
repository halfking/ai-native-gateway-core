-- 869_promote_session_turns_trace_events.sql
-- 修复 session_turns_hot → 月分区晋级路径的**规模化静默数据丢失**。
--
-- 缺陷（2026-10-16 retire 轮，本机 PG 只读复核）：
--   promote_session_turns_hot_to_partition 的 INSERT/RETURNING 列清单是硬编码的
--   54 列，而 public.session_turns_hot 实有 **106 列**（两侧各 106，集合全等）。
--   晋级只搬运 54 列，其余 **52 列在行离开 hot 落进月分区时被静默丢弃**，
--   函数不报错、返回 moved 计数照常递增。trace_events 只是其中之一。
--
--   实测被丢的 52 列（hot 有 / 清单无，按 attnum 序）：
--     request_delta, response_delta, is_final_success, api_key_id,
--     application_id, end_user_id, customer_id, credits_charged, cost_display,
--     cost_currency, work_type, token_band, usage_source, is_auto_request,
--     auto_decision, auto_confidence, task_type_chosen, routing_attempts,
--     routing_summary, canonical_id, canonical_model, raw_model_name,
--     trace_events, failure_stage, failure_detail_code, upstream_status_code,
--     upstream_finish_reason, stream_first_chunk_ms, stream_chunk_count,
--     stream_interrupted, stream_done_sent, client_request_id, client_endpoint,
--     client_timeout, egress_protocol, search_text, request_preview,
--     response_preview, transform_summary, identity_hash, request_checksum,
--     response_checksum, system_fingerprint, origin_stage, origin_actor,
--     client_ip, client_forwarded_for, agent_name, agent_type,
--     virtual_client_id, is_abandoned, request_status
--
--   本机存量证据（修复前，只读）：
--     session_turns      229 行 / count(trace_events) = 0
--     session_turns_hot  264 行 / count(trace_events) = 264
--   即热表里**每一行**都带 trace_events，而历史晋级进来的 229 行**一列都没有**。
--   v1 停写后 trace 只存在于 session 家族，故这是规模化损失而非边角。
--
-- 根因（不是「707 漏了 trace_events」，而是 707 的修复被覆盖）：
--   707_session_turns_s1a.sql 早已把本函数改写成**目录派生列清单**
--   （v_cols 取自 pg_attribute，DELETE ... RETURNING h.* → INSERT 按名映射）
--   并带 hot/parent 列形状守卫，它能正确搬运全部 106 列。
--   但 640_session_turns_protocol_fields.sql 为了给 713 补 client_protocol
--   前置而被登记进本通道清单，且**排在 707 之后**（清单 707 在前、640 在后），
--   于是 640 那份**旧的硬编码 54 列函数体**每次重放都把 707 的修复覆盖掉。
--   apply-db-revision-sequence.sh 的 intentional_function_chains 甚至把这条
--   链登记成 `707 → 640`（640 为终态），把覆盖固化成了「有意链」。
--   installer 全新安装通道顺序是 640(391) → 688(477) → 707(493)，707 在最后，
--   所以**全新安装是对的、存量库/部署库是错的**——两侧行为分叉的根源在此。
--
-- 改法：
--   把目录派生列清单 + 列形状守卫**重新确立为本通道的终态体**（869 排在 640
--   之后并登记为链尾），不再依赖「707 在清单里更早」这种易被后来补登的
--   前置迁移顶掉的顺序巧合。今后任何对称 ADD COLUMN 自动流经晋级，
--   缺列/类型漂移在守卫处直接报错，绝不静默丢列。
--
-- 语义保持（本迁移**唯一**的行为变化就是搬运的列集合）：
--   * promotion batch（session_turns_promotion_batch / LIMIT p_batch_size /
--     ORDER BY h.ts, h.id, h.partition_date）原样；
--   * 重复行预检（v_conflicts + RAISE WARNING）与 NOT EXISTS 过滤原样；
--   * session/request 两级 advisory lock 原样；
--   * ensure_sessions_v2_partitions + 月分区挂载校验原样；
--   * **默认 retention 保持线上现值 '7 days'**（见下方「刻意不改」）。
--
-- 刻意不改（留给 owner 拍板，不在本迁移范围内）：
--   707/688 的默认值是 '8 hours'，与 Go 侧 bg/partition_manager.go 的
--   DefaultRetentionWindow = 8 * time.Hour 对齐；线上现值 '7 days' 是 640
--   覆盖带来的副产物。调度器始终显式传参，默认值只对手工 promote 生效。
--   改它会把热表窗口收窄 21 倍，属行为变更而非数据丢失修复，故此处按
--   **保留线上现值 '7 days'** 处理，只修列集合这一件事。

BEGIN;

SET LOCAL lock_timeout = '5s';

-- 与 640 的差异仅两处：列清单改为目录派生（v_cols），列形状守卫改为
-- string_agg 形状比对。其余逻辑逐字保留。
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

    -- 869：列形状守卫（沿用 707 的 name:type:notnull 形状串）。
    -- 两侧（父表 / hot）任一侧缺列、多列、类型漂移或非空约束漂移都在此
    -- 直接报错；配合下面的目录派生列清单，对称 ADD COLUMN 自动流经晋级，
    -- 不对称的形状漂移则响亮失败，两头都不再静默丢列。
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

    -- 目录派生列清单（hot 的 attnum 序）。INSERT 按名映射，故父表/hot 列序
    -- 漂移不影响正确性；DELETE ... RETURNING h.* 的输出同样是 attnum 序，
    -- 与本清单逐位对应。
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

    -- 869：目录派生列清单 + 按名映射，搬运 hot 的全部非丢弃列。
    -- 修复前这里是硬编码的 54 列硬编码清单，52 列（含 trace_events）被静默丢弃。
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
  'Move cold rows from session_turns_hot to monthly partitions. 869 re-asserts the catalog-derived column list (707 design) as this channel''s terminal body: the hardcoded 54-column list silently dropped 52 columns (trace_events, is_final_success, request_delta, identity_hash, ...) on every promotion. Columns are now derived from pg_attribute and mapped by name, with a hot/parent shape guard, so symmetric ADD COLUMN flows through and asymmetric drift raises instead of dropping data.';

-- 后置条件：形状两侧全等，且函数体确为目录派生（防止本迁移被再次覆盖时
-- 静默「安装成功」——这一条正是本次事故当初缺失的那道牙）。
DO $post_869$
DECLARE
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
    v_prosrc TEXT;
BEGIN
    IF to_regclass('public.session_turns_hot') IS NULL
       OR to_regclass('public.session_turns') IS NULL THEN
        RAISE EXCEPTION '869 post-condition failed: session_turns family missing';
    END IF;

    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent_shape FROM pg_attribute
     WHERE attrelid = 'public.session_turns'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot_shape FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
        RAISE EXCEPTION '869 post-condition failed: session_turns hot/parent column sets diverge';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum) INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_cols IS NULL OR v_cols NOT LIKE '%trace_events%' THEN
        RAISE EXCEPTION '869 post-condition failed: derived column list lacks trace_events';
    END IF;

    SELECT p.prosrc INTO v_prosrc
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.proname = 'promote_session_turns_hot_to_partition'
       AND pg_get_function_identity_arguments(p.oid) = 'p_retention interval, p_batch_size integer';
    IF v_prosrc IS NULL THEN
        RAISE EXCEPTION '869 post-condition failed: promote_session_turns_hot_to_partition missing';
    END IF;
    IF v_prosrc NOT LIKE '%v_cols%' OR v_prosrc NOT LIKE '%INSERT INTO public.session_turns (%s)%' THEN
        RAISE EXCEPTION '869 post-condition failed: installed body is not the catalog-derived form';
    END IF;
    -- 派生清单本身不得按列名过滤：只断言「是目录派生形态」不够——有人在
    -- v_cols 查询里加一个 attname <> 'trace_events' 这类过滤，形态检查照样
    -- 通过，而数据照样被丢（2026-10-16 变异验证实测：仅靠上一条断言时
    -- 注入该过滤的 869 仍然「安装成功」）。这条断言把该变异挡在部署期。
    IF v_prosrc ~ 'INTO\s+v_cols[^;]*attname\s*(<>|!=)\s*' THEN
        RAISE EXCEPTION '869 post-condition failed: derived column list is filtered by column name';
    END IF;
END
$post_869$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('869', 'promote_session_turns_hot_to_partition: re-assert catalog-derived column list as terminal body (hardcoded 54-col list silently dropped 52 cols incl. trace_events)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
