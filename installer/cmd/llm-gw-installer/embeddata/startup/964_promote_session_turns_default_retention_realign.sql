-- 964_promote_session_turns_default_retention_realign.sql
-- 2026-11-13（全面审计v3 第 53 增量移交清单 F-43 收口的最后一步）：
--   把 promote_session_turns_hot_to_partition 的签名默认保留期
--   **'7 days' → '8 hours'**（与 Go 调度器 DefaultRetentionWindow =
--   lifecycle.hot_retention_hours 默认 8 重新对齐）。
--
-- ── 为什么叫「realign」：★ 这不是首次对齐，是把「688 对齐 → 意外覆盖 → 有意保守」
--   的链走完 ────────────────────────────────────────────────────────────────
--   688_promote_default_retention_align_go_scheduler.sql（2026-09-08）曾把本函数
--   DEFAULT '7 days' → '8 hours'（函数体整段复制自 640）；707 重写时保持 8h
--   （其头注释自陈「默认值对齐 688 的 8h/5000」）。
--   但 640 被登记进本通道且**排在 707 之后**（869 头注释记录的投递序事故）
--   ⇒ 640 那份旧形态函数体（含 '7 days' 签名）把 688/707 的对齐**覆盖**掉。
--   之后 869 重写时（基线 = 707 的目录派生体）**有意保守**：把「在役默认」
--   '7 days' 原样保留（migration_869_test.go:96 钉死「must preserve the deployed
--   default retention ('7 days')」，理由 = 数据修复不混行为变更）——
--   ★ 但「在役值」本身就是被 640 事故覆盖出来的错值 ⇒ 有意保守保留的是错值。
--   897（基线 = 869）沿袭。
--   ⇒ 现场实测（本迁移落地前）：全仓 10 个 688 对齐过的 promote 函数里，
--     **唯独本函数**的现行载体（897）仍是 '7 days'；其余 9 个经 698/739/935
--     重写都保持了对齐后的默认值。
--
-- ── 影响（★ 与 F-43/F-58 台账一致）────────────────────────────────────────
--   · PartitionManager 每次 promote tick 显式传参（$1::interval, $2::int）
--     ⇒ **自动调度零行为变化**；
--   · admin 手动 promote 端点（data_lifecycle_hot_partition.go runHotPromoteJob）
--     也显式传参（retentionInterval = %d hours）⇒ **端点零行为变化**；
--   · 唯一受影响面 = 运维**裸调** `SELECT promote_session_turns_hot_to_partition();`
--     不传参时：修复前多保留 21 倍 hot 数据（与「8 小时不变式」相悖），
--     修复后与调度器一致。
--   · F-58 的教训面：admin/logs.go 的注释曾引用本函数签名默认 '7 days' 当论据
--     （0-15 的 hotLegMaxWindow=7d 即由此误信）⇒ 签名默认与生产行为背离是
--     「注释系统性失真」的源头之一（LESSONS §702①）。本迁移让签名默认**说真话**。
--
-- ── 函数体零改动（★ 只改签名默认常量）────────────────────────────────────
--   函数体整段复制自 **897**（现行体：目录派生列清单 + 形状守卫 + final_success
--   demote 自愈），除 DEFAULT 一行外**逐字不变**——门
--   sql/migrations/startup/migration_964_promote_default_realigned_test.go
--   会把「897 + 一处 DEFAULT 差异」钉成唯一允许的 delta，其余任何漂移都红。
--
-- Idempotent: YES（纯 CREATE OR REPLACE FUNCTION + 后置条件断言）。
-- Down: 可执行（恢复 897 的 '7 days' 形态）。回滚后果如实登记：
--   回滚后裸调不传参的默认窗口重新放大到 7 天（回到 F-43 登记的形态）；
--   自动调度与显式传参路径不受影响。
-- ★ 编号说明：移交清单曾按「候选迁移 961」登记本项，但 961 已被
--   961_market_scoring_runs.sql（A-17 M2-T3 评分 job 轮）与 963_credential_total_requests_rollup（2026-11-13 24h 审计轮，并行已推）先后占用 ⇒ 本迁移取 964。

BEGIN;

SET LOCAL lock_timeout = '5s';

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
  'Move cold rows from session_turns_hot to monthly partitions. 897 keeps 869''s catalog-derived column list and adds the final-success self-heal (695 design): the hardcoded 54-column list silently dropped 52 columns (trace_events, is_final_success, request_delta, identity_hash, ...) on every promotion. Columns are now derived from pg_attribute and mapped by name, with a hot/parent shape guard, so symmetric ADD COLUMN flows through and asymmetric drift raises instead of dropping data. 897 adds the demote step that 695 introduced for the v1 request_logs family: before the batch is selected, a hot row whose (tenant_id, session_id, partition_date) already holds is_final_success in the parent is demoted to FALSE, so the batch INSERT can no longer violate uq_session_turns_final_success (23505) and roll the whole batch back. 964 realigns the p_retention signature default to 8 hours: the 688 alignment was overwritten when a channel-ordering accident replayed the pre-688 640 body after 707, and 869 then deliberately preserved the deployed (wrong) value; the Go scheduler and the admin manual endpoint always pass explicit arguments, so the default only affects bare ad-hoc calls.';

DO $post_964$
DECLARE
    v_parent_shape TEXT;
    v_hot_shape TEXT;
    v_cols TEXT;
    v_prosrc TEXT;
    v_fndef TEXT;
BEGIN
    IF to_regclass('public.session_turns_hot') IS NULL
       OR to_regclass('public.session_turns') IS NULL THEN
        RAISE EXCEPTION '964 post-condition failed: session_turns family missing';
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
        RAISE EXCEPTION '964 post-condition failed: session_turns hot/parent column sets diverge';
    END IF;

    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum) INTO v_cols
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_cols IS NULL OR v_cols NOT LIKE '%trace_events%' THEN
        RAISE EXCEPTION '964 post-condition failed: derived column list lacks trace_events';
    END IF;

    SELECT p.prosrc INTO v_prosrc
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.proname = 'promote_session_turns_hot_to_partition'
       AND pg_get_function_identity_arguments(p.oid) = 'p_retention interval, p_batch_size integer';
    IF v_prosrc IS NULL THEN
        RAISE EXCEPTION '964 post-condition failed: promote_session_turns_hot_to_partition missing';
    END IF;
    IF v_prosrc NOT LIKE '%v_cols%' OR v_prosrc NOT LIKE '%INSERT INTO public.session_turns (%s)%' THEN
        RAISE EXCEPTION '964 post-condition failed: installed body is not the catalog-derived form';
    END IF;
    -- 派生清单本身不得按列名过滤（897 的牙，原样保留）。
    IF v_prosrc ~ 'INTO\s+v_cols[^;]*attname\s*(<>|!=)\s*' THEN
        RAISE EXCEPTION '964 post-condition failed: derived column list is filtered by column name';
    END IF;
    -- ★ 897 的牙：**demote 步必须在位**（原样保留）。
    IF v_prosrc NOT LIKE '%SET is_final_success = FALSE%' THEN
        RAISE EXCEPTION '964 post-condition failed: demote step missing (the self-heal was not installed)';
    END IF;

    -- ★★ 964 自己的牙：**签名默认必须是 8 小时形态**。
    --   本迁移的全部语义就落在这一行 ⇒ 没有它 ⇒ 本迁移只是把 897 重新装了一遍
    --   ⇒ 「安装成功」掩盖「什么也没修」（897 的 demote 牙同款教训）。
    --   判据用 pg_get_functiondef（PG 把 '8 hours' 归一化渲染为 '08:00:00'::interval，
    --   本迁移落地前已在临时夹具库实测），并显式拒绝旧形态 '7 days' 回流。
    SELECT pg_get_functiondef(p.oid) INTO v_fndef
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.proname = 'promote_session_turns_hot_to_partition'
       AND pg_get_function_identity_arguments(p.oid) = 'p_retention interval, p_batch_size integer';
    IF position('DEFAULT ''08:00:00''::interval' in v_fndef) = 0 THEN
        RAISE EXCEPTION '964 post-condition failed: p_retention default is not the 8-hour form';
    END IF;
    IF position('7 days' in v_fndef) > 0 THEN
        RAISE EXCEPTION '964 post-condition failed: stale 7-day default is back';
    END IF;
END
$post_964$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('964', 'promote_session_turns_hot_to_partition: realign the p_retention signature default to 8 hours — the 688 alignment was overwritten by a 640 replay-ordering accident and then deliberately preserved at the deployed (wrong) value by 869 (pinned by migration_869_test.go) and 897; body is byte-identical to 897 except that one constant')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
