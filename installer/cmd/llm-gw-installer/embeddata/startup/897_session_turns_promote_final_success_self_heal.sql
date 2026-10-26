-- 897_session_turns_promote_final_success_self_heal.sql
-- 2026-10-23（§143.96 轮）：给会话族的 hot 排水补上 **final_success 自愈**
--   （★ 迁移 695 为 v1 request_logs 族做过同款；★ 那个族后来被 890 退役，
--   ★ 自愈没有随族迁走 ⇒ ⇒ 真正有同款病灶的会话族一直没有它）。
--
-- ── 根因（★ 生产定点实测，2026-10-23 11:35，252 共享库）────────────────
--   父表与 hot 表各有同构的部分唯一索引：
--     session_turns      : uq_session_turns_final_success
--                          UNIQUE (tenant_id, session_id, partition_date) WHERE is_final_success
--     session_turns_hot  : uq_session_turns_hot_final_success（同键同谓词）
--   而 promote 的**去重**键是 (tenant_id, request_id) ⇒ ⇒ **它看不见上面那个键**
--   ⇒ ⇒ 于是：
--     ① 写侧 markTurnFinalSuccess 给 hot 侧打 is_final_success=TRUE
--     ② 该行过 8h 保留窗后被 promote 搬进父表 ⇒ 父表此刻**已持有**该键的 mark
--     ③ hot 侧那条**已不在 hot** ⇒ hot 的部分唯一索引**看不见它**
--        ⇒ ⇒ 同一会话同一天的第二条 fs claim 被**放行**
--     ④ promote 的去重键（request_id）也看不见 ⇒ ⇒ 批次里带着它
--     ⑤ INSERT 撞 uq_session_turns_final_success ⇒ **23505**
--        ⇒ ⇒ 原子 CTE **整批回滚、零行搬走**
--        ⇒ ⇒ 且批次按 ts 升序 ⇒ 冲突行**每批必含** ⇒ ⇒ **每个 tick 必败**
--
-- ── 实测（★ 同一键 default/gw_omp-global-kxpms/2026-10-22）──────────────
--   父表那条（先到者）id 1,604,654 ts 2026-10-22 15:37:53.744145 turn_no 20
--   hot  那条（后到者）id 1,612,123 ts 2026-10-22 23:48:08.681584 turn_no 1553
--   ⇒ 相隔 8h10m15s（★ 正好跨过 8h promote 边界 ⇒ 长活会话的典型形态）
--   ⇒ 致命行越窗时刻 2026-10-23 07:48:08.681584
--   ⇒ 07:46:23 那 tick 成功（rows=1168，★ 差 2 分钟它还没越窗）
--   ⇒ 08:47:49 那 tick 报 23505（★ 第一次把它纳入批次）
--   ⇒ 09:49:23 又 23505；06:44:14 与 10:51:30 报 timeout（★ 积压变大后的第二种死法）
--
-- ── 后果（★ 两条都实测）──────────────────────────────────────────────
--   ① hot 表无限涨：2,843 行已超 8h 保留窗、写入速率 ~850 行/小时
--   ② **父表饿死**：hot 的 n_tup_ins 210,763 ≈ 父表 2026_10 的 212,347
--      ⇒ ⇒ 父表的**唯一**写入路径就是 promote ⇒ ⇒ promote 一停，父表彻底不收冷行
--
-- ── 修法（★ 与 695 逐字同款：★ 在批次选取【之前】降级输家）────────────
--   父表那条（先到者）站住；hot 这条（后到者）降级为 is_final_success = FALSE
--   ⇒ 语义「输家 = superseded，历史不改写」⇒ ⇒ 与 695 一致 ⇒ ⇒ 零语义变更
--   ⇒ ⇒ 降级后批次 INSERT 不再撞 23505 ⇒ ⇒ 整批提交 ⇒ ⇒ 排水恢复
--
-- ★ 本迁移的**基线是 869**（★ 现行体：★ 526 → 636 → 640 → 688 → 707 → 869）
--   ⇒ 函数体整段来自 869，★ **只插入一处 demote 步** ⇒ ⇒ 不引入其它差异
--   ⇒ ⇒ 并保留 869 的「目录派生列清单 + 形状守卫」两道牙。
--
-- ★ 为什么不改写侧的 claim：★ 那属于**预防**（★ 另一个落点，★ 需属主另拍板），
--   ★ 而本迁移是**数据面自愈**（★ 与 695 同款）⇒ ⇒ 两者可独立推进。
--
-- Idempotent: YES（纯 CREATE OR REPLACE FUNCTION + 后置条件断言；重复应用无副作用）
-- Down: 无（★ 与 695 同款）。紧急回滚 = 重放 869 的函数体定义
--       （★ 仓内 sql/migrations/startup/869_promote_session_turns_trace_events.sql
--       ★ 即该定义；★ 本目录另附 897_*.down.sql 的等价回滚体）。
-- ★★ 本迁移**只在本目录与启动链里落地**；★ 生产应用需属主另行授权。

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
    v_demoted BIGINT := 0;
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


    -- ── 自愈步（★ 对齐迁移 695 的做法；★ 详见 897 头注释）──────────────
    -- 语义：**父表那条（先到者）站住**；hot 这条（后到者）降级为 FALSE
    --（★ 与 695 逐字同款：「输家 = superseded，历史不改写」）。
    -- 位置：**批次选取之前** ⇒ 降级后它才带着 FALSE 进批次
    --        ⇒ INSERT 不再撞 uq_session_turns_final_success
    --        ⇒ 整批不再回滚 ⇒ 排水恢复。
    -- 范围：只碰**已过保留窗**的行（h.ts < 保留窗）⇒ ⇒ 新写入不受影响。
    -- 成本：EXISTS 走 uq_session_turns_final_success（部分唯一索引）
    --        且 partition_date 是分区键 ⇒ ⇒ 一次索引探针 + 分区裁剪。
    UPDATE public.session_turns_hot h
       SET is_final_success = FALSE
     WHERE h.is_final_success IS TRUE
       AND h.ts < statement_timestamp() - p_retention
       AND EXISTS (
           SELECT 1
           FROM public.session_turns p
           WHERE p.tenant_id = h.tenant_id
             AND p.session_id = h.session_id
             AND p.partition_date = h.partition_date
             AND p.is_final_success IS TRUE
       );
    GET DIAGNOSTICS v_demoted = ROW_COUNT;
    IF v_demoted > 0 THEN
        RAISE WARNING 'session_turns promote demoted % hot final-success rows (parent already holds the mark)',
            v_demoted;
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
  'Move cold rows from session_turns_hot to monthly partitions. 897 keeps 869''s catalog-derived column list and adds the final-success self-heal (695 design): the hardcoded 54-column list silently dropped 52 columns (trace_events, is_final_success, request_delta, identity_hash, ...) on every promotion. Columns are now derived from pg_attribute and mapped by name, with a hot/parent shape guard, so symmetric ADD COLUMN flows through and asymmetric drift raises instead of dropping data. 897 adds the demote step that 695 introduced for the v1 request_logs family: before the batch is selected, a hot row whose (tenant_id, session_id, partition_date) already holds is_final_success in the parent is demoted to FALSE, so the batch INSERT can no longer violate uq_session_turns_final_success (23505) and roll the whole batch back.';

DO $post_897$
DECLARE
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
    v_prosrc TEXT;
BEGIN
    IF to_regclass('public.session_turns_hot') IS NULL
       OR to_regclass('public.session_turns') IS NULL THEN
        RAISE EXCEPTION '897 post-condition failed: session_turns family missing';
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
        RAISE EXCEPTION '897 post-condition failed: session_turns hot/parent column sets diverge';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum) INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_cols IS NULL OR v_cols NOT LIKE '%trace_events%' THEN
        RAISE EXCEPTION '897 post-condition failed: derived column list lacks trace_events';
    END IF;

    SELECT p.prosrc INTO v_prosrc
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.proname = 'promote_session_turns_hot_to_partition'
       AND pg_get_function_identity_arguments(p.oid) = 'p_retention interval, p_batch_size integer';
    IF v_prosrc IS NULL THEN
        RAISE EXCEPTION '897 post-condition failed: promote_session_turns_hot_to_partition missing';
    END IF;
    IF v_prosrc NOT LIKE '%v_cols%' OR v_prosrc NOT LIKE '%INSERT INTO public.session_turns (%s)%' THEN
        RAISE EXCEPTION '897 post-condition failed: installed body is not the catalog-derived form';
    END IF;
    -- 派生清单本身不得按列名过滤：只断言「是目录派生形态」不够——有人在
    -- v_cols 查询里加一个 attname <> 'trace_events' 这类过滤，形态检查照样
    -- 通过，而数据照样被丢（2026-10-16 变异验证实测：仅靠上一条断言时
    -- 注入该过滤的 869 仍然「安装成功」）。这条断言把该变异挡在部署期。
    IF v_prosrc ~ 'INTO\s+v_cols[^;]*attname\s*(<>|!=)\s*' THEN
        RAISE EXCEPTION '897 post-condition failed: derived column list is filtered by column name';
    END IF;
    -- ★★ 897 自己的牙：**demote 步必须在位**。
    --   ★ 没有它 ⇒ 本迁移只是把 869 重新装了一遍 ⇒ ⇒ 23505 照旧 ⇒
    --   ⇒ 而「安装成功」会掩盖「什么也没修」。
    IF v_prosrc NOT LIKE '%SET is_final_success = FALSE%' THEN
        RAISE EXCEPTION '897 post-condition failed: demote step missing (the self-heal was not installed)';
    END IF;
END
$post_897$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('897', 'promote_session_turns_hot_to_partition: add the 695-style final-success self-heal (demote step) so the batch INSERT cannot violate uq_session_turns_final_success (23505) and roll the whole batch back')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
