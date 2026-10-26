-- 897_session_turns_promote_final_success_self_heal.down.sql
-- 回滚 = **重放 869 的函数体**（★ 即「撤掉 demote 步」）。
--
-- ★ 为什么要给 down 而不是学 695 写「无」：★ 695 写「无」是因为它的基线
--   （688）在 v1 族退役后已不在生产链上 ⇒ ⇒ 没人需要回滚它；
--   ★ 而 897 的基线（869）**仍在生产链上** ⇒ ⇒ 回滚路径应当是**可执行**的。
--
-- ★ 幂等：YES（纯 CREATE OR REPLACE FUNCTION）。
-- ★ 语义后果（★ 如实登记）：★ 回滚后 23505 会**重新出现** ⇒ ⇒
--   ⇒ hot 表会重新开始堆积、★ 父表会重新停止接收冷行
--   ⇒ ⇒ **回滚不是「回到无害」，★ 是「回到故障」** ⇒ ⇒ 除非另有安排，★ 不要回滚。

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

COMMIT;
