-- 830: ursm_node_snapshot_min 改为按日 RANGE 分区表 + DROP 型留存的分区底座
--
-- ★★★ 手工执行迁移，**不在 installer 自动启动序列里** ★★★
--     本文件刻意**没有**注册到：
--       · installer/cmd/llm-gw-installer/main.go 的 //go:embed + embeddedSQLFiles map
--       · installer/internal/dbinit/runner.go 的启动文件清单
--       · scripts/apply-db-revision-sequence.sh 的升级通道
--     ⇒ 下次任何人跑 installer / 升级通道，本文件**不会**被自动应用。
--     执行方式见设计稿 §5.2「迁移步骤」，由人在可控窗口手工跑：
--       psql "$DSN" -X -v ON_ERROR_STOP=1 -f sql/migrations/startup/825_ursm_node_snapshot_min_partitioned.sql
--
-- 为什么不自动应用（2026-10-04 决定）：步骤 1 会对一张 10 GB 的活表做
-- RENAME。放进自动序列意味着 154/245 任何一次无人值守的 installer 升级
-- 都会在无人工确认点的情况下执行它。手工化把执行时机交回给人。
--
-- ── 背景 ─────────────────────────────────────────────────────────────
-- 这张表 10 GB / 占库 45%，写入 3.22M 行/天（37.3 行/秒），
-- 现有留存是按行分批 DELETE。DELETE 只把行变成死元组，空页仍留在堆的
-- 物理头部，VACUUM 无法截断 ⇒ 磁盘单调增长（设计稿 §1.3）。
-- 分区化本身不改变「按行删」这一事实；必须同时把留存换成 DROP 分区
-- （Go 侧已落地：domains/ursm/v2/persist/retention_partition.go，
--  按 pg_class.relkind 自动选路，迁移前走 DELETE、迁移后走 DROP）。
--
-- 设计稿：docs/06-deployment/04-runbooks/runbooks/
--         2026-10-04-ursm-snapshot-partitioning-design.md（§3.5 DDL / §5.2 步骤 / §7 V1~V7）
--
-- ── 硬约束（改动前必读）─────────────────────────────────────────────
-- 1. **PK 必须保留**。writer.go:402 的
--      ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING
--    要求存在恰好覆盖这四列的唯一索引作 arbiter。删掉 PK ⇒ 全量写入瘫痪
--    （报 there is no unique or exclusion constraint matching the
--    ON CONFLICT specification）。所以下面连同分区键一起建 PK。
-- 2. **不建 DEFAULT 分区**。全仓无 snapshot_ts 单列读路径，DEFAULT
--    只会变成永远扫到的垃圾堆；且有 DEFAULT 时 PG 对「父表挂 DEFAULT 又
--    建具体分区」会先校验 DEFAULT 内有无越界行 —— 750 就在这上面踩过
--    （用法是 move-then-attach）。本表无 DEFAULT，故不存在该约束校验，
--    可以直接 CREATE TABLE ... PARTITION OF。
-- 3. **必须预建当日分区**。无 DEFAULT ⇒ 今天的分区不存在时
--    writer 100% 失败（no partition of relation found），不是降级。
--    本文件预建当日；次日/后日由 bg/partition_manager.go 的 ensureSpecs
--    24h tick 接管（该条目已随 Go 代码提交）。
-- 4. **分区名格式是契约**：`ursm_node_snapshot_min_YYYYMMDD`。
--    DROP 型留存按这个名字里的日期判过期（retention_partition.go 的
--    `_([0-9]{8})$` 匹配）。手工建分区若不遵守，永远不会被清理 ——
--    后果是空间不回收，不是数据丢失。
-- 5. **时区必须显式钉住**。函数级 SET timezone = 'Asia/Shanghai' 与
--    预建调用的 (now() AT TIME ZONE 'Asia/Shanghai') 同源。UTC 会话会把
--    分区边界挪 8 小时，而 DROP 判据正是这个边界 ⇒ 提前删掉最多 8 小时
--    的存活数据。694/751 同族教训。

-- ===========================================================================
-- 步骤 0：前置守卫（fail-closed）
--   任一条件不满足即 RAISE，中止在改动之前。幂等重放**不安全**（见下）。
-- ===========================================================================
DO $$
BEGIN
    IF to_regclass('public.ursm_node_snapshot_min_legacy') IS NOT NULL THEN
        RAISE EXCEPTION
            'ursm_node_snapshot_min_legacy 已存在 —— 本迁移已执行过，不要重放。'
            '如需重来请先人工确认两表状态。';
    END IF;

    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RAISE EXCEPTION
            'public.ursm_node_snapshot_min 不存在 —— 期望它已由 01-schema.sql + 818 建立。';
    END IF;

    -- 已被别处（上一轮手工执行）改成分区表时也要挡住。
    IF EXISTS (SELECT 1 FROM pg_class
                WHERE oid = 'public.ursm_node_snapshot_min'::regclass
                  AND relkind = 'p') THEN
        RAISE EXCEPTION
            'public.ursm_node_snapshot_min 已是分区父表（relkind=p）—— 无需再迁移。';
    END IF;
END $$;

-- ===========================================================================
-- 步骤 1：单事务原子切换
--   ★ 为什么必须单事务：拆成「改名 / 建父表 / 建首个分区」三步后，
--     「父表已建、分区未建」的窗口里 writer 会 100% 失败（无 DEFAULT 兜底）。
--     单事务把这个窗口消掉。
--   ★ RENAME 是纯目录操作，不重写数据、不搬行，所以整个事务是毫秒级；
--     期间的并发 INSERT 只是在 ACCESS EXCLUSIVE 锁上排队，提交后立即
--     成功并路由进新表 —— 不丢数据、不需要停机。
--   ★ lock_timeout：拿不到锁就快速失败，绝不把线上写入堵在锁队列里。
-- ===========================================================================
BEGIN;
SET LOCAL lock_timeout = '10s';

-- 1a) 旧表改名保留。历史不丢，运维仍可查，回滚只需改回来。
ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_legacy;

-- 1a-2) ★ 把旧 PK 约束改名，让出 canonical 名字。
--   踩坑记录（2026-10-04，本地 PG 17.11 实跑才暴露）：约束在 RENAME 时
--   **跟着表走、名字不变**，所以 `_legacy` 仍然占着
--   `ursm_node_snapshot_min_pkey`。下面 1c 用同名给新父表建 PK 时直接
--   报 `relation "ursm_node_snapshot_min_pkey" already exists`。
--   改名而不是删除：删了虽然也能过，但 `_legacy` 就失去 PK，
--   回滚后 writer 的 ON CONFLICT 失去 arbiter —— 那是硬约束 1 说的
--   「删了 PK 全量写入瘫痪」，只是延迟到回滚那天才爆。
--   ★ 这一步必须**幂等**：up→down→up 往返时，down 已把约束名换回
--   `ursm_node_snapshot_min_pkey`，再执行本条会报
--   `constraint "ursm_node_snapshot_min_pkey" ... does not exist`
--   （本地 PG 17.11 往返实测踩到）。只能退不能进的回滚是假回滚。
--   所以先读实际名字，只在还是 canonical 名时才改名。
DO $$
DECLARE
    cur_name TEXT;
BEGIN
    SELECT conname INTO cur_name
      FROM pg_constraint
     WHERE conrelid = 'public.ursm_node_snapshot_min_legacy'::regclass
       AND contype = 'p'
     LIMIT 1;

    IF cur_name IS NULL THEN
        RAISE EXCEPTION 'ursm_node_snapshot_min_legacy 上找不到主键约束 —— 状态异常，人工确认。';
    END IF;

    IF cur_name = 'ursm_node_snapshot_min_pkey' THEN
        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min_legacy
                 RENAME CONSTRAINT ursm_node_snapshot_min_pkey
                 TO ursm_node_snapshot_min_legacy_pkey';
    END IF;
    -- 已是 ursm_node_snapshot_min_legacy_pkey（回滚后再上的情形）⇒ 无需动作。
END $$;

-- 1b) 分区父表。
--     56 列 = 01-schema.sql 的 32 列 + 818 的 24 列。
--     ★ 依赖 818 已应用（否则下面 24 列不存在 writer 写入就会失败）。
CREATE TABLE public.ursm_node_snapshot_min (
    snapshot_ts timestamp with time zone NOT NULL,
    recovery_epoch bigint NOT NULL,
    provider_id integer NOT NULL,
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    canonical_name text,
    tenant_id text DEFAULT ''::text NOT NULL,
    available boolean NOT NULL,
    health_status text,
    fail_streak integer,
    cool_until timestamp with time zone,
    sr_1m real,
    sr_5m real,
    sr_30m real,
    samples_1m integer,
    samples_5m integer,
    samples_30m integer,
    lat_p50_ms integer,
    lat_p95_ms integer,
    score real,
    price_in_per_1m numeric,
    price_out_per_1m numeric,
    billing_mode text,
    trust_level real,
    baseurl_latency_ms integer,
    conc_used integer,
    conc_limit integer,
    fp_used integer,
    fp_limit integer,
    source_priority integer,
    generation bigint,
    payload jsonb,
    -- 以下 24 列来自 818 迁移（基线 01-schema.sql 尚未收录，设计稿 §9 Finding A）
    updated_at_ms bigint,
    last_probe_at_ms bigint,
    last_probe_latency_ms bigint,
    last_attempt_ms bigint,
    last_ok_ms bigint,
    last_request_at_ms bigint,
    last_request_error_at_ms bigint,
    manual_at_ms bigint,
    cool_until_ms bigint,
    event_seq bigint,
    disabled boolean,
    last_direct_ok boolean,
    manual_hold boolean,
    success_count integer,
    failure_count integer,
    disable_count integer,
    lat_ewma_ms real,
    empty_response_rate_1m real,
    empty_response_rate_30m real,
    last_err text,
    manual_reason text,
    manual_actor text,
    disabled_reason text,
    cool_reason text
) PARTITION BY RANGE (snapshot_ts);

-- 1c) 主键。保留且四列不变（见文件头硬约束 1）。
--     PG 要求父表唯一索引含全部分区键列；snapshot_ts 已是首列 ⇒ 合法。
--     ★ 刻意不建 ursm_node_snapshot_min_ts_idx：全仓无 snapshot_ts 单列
--       读路径（设计稿 §5.1），留存改为 DROP 后它彻底无用。
--       （实测旧表该索引 144 MB / 7,133 次扫描但产出 21.28 亿行，
--         即它承担的是范围扫描，删不得的是 PK 之外的读路径；
--         而新表上这类扫描由 DROP 留存取代。）
ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey
    PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

-- 1d) 按日 ensure 函数（照 750 范式 + 751 时区钉扎）。
CREATE OR REPLACE FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(p_date DATE)
RETURNS void LANGUAGE plpgsql SET timezone = 'Asia/Shanghai' AS $$
DECLARE
    pname    TEXT := format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYYMMDD'));
    start_ts TIMESTAMPTZ := p_date::timestamptz;
    end_ts   TIMESTAMPTZ := (p_date + INTERVAL '1 day')::timestamptz;
BEGIN
    -- 幂等短路：分区已挂接则无事可做。
    IF EXISTS (
        SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 并发串行化：boot ensure / installer / 升级通道 / 24h tick 在
    -- 252 共享库上会并发。
    PERFORM pg_advisory_xact_lock(
        hashtext('ensure_ursm_node_snapshot_min_daily_partition:' || pname));

    -- 二次短路：拿到锁后可能已被别的入口建好。
    -- ★ 短路条件必须是「已挂在**本父表**下」，不是「同名对象存在」。
    --   本地 PG 17.11 往返实测（up→down→up）暴露的静默故障：
    --   `ursm_node_snapshot_min_YYYYMMDD` 仍挂在 _post825 下时，
    --   按名字短路会让新建的父表**一个分区都没有**；随后每次写入都报
    --      ERROR: no partition of relation "ursm_node_snapshot_min" found for row
    --   而迁移 RC=0、ensure 返回成功、日志无任何异常 ——
    --   静默 + 致命，且要等到真的写入才暴露。
    IF EXISTS (
        SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
          AND c.relname = pname
    ) THEN
        RETURN;
    END IF;

    -- 同名对象存在却不挂在本父表下 ⇒ 明确报错，绝不静默跳过。
    --   「宁可吵，也不要安静地留一个零分区的分区父表」。
    IF to_regclass('public.' || pname) IS NOT NULL THEN
        RAISE EXCEPTION
            '分区名 % 已被占用，但它不是 public.ursm_node_snapshot_min 的子分区。'
            '若直接跳过，新建的父表将没有任何分区，之后每次写入都会报 '
            'no partition of relation found，而迁移仍会报成功。'
            '请先 DROP 或改名该对象（常见来源：830.down 保留的 _post825）。',
            pname;
    END IF;

    -- 本表无 DEFAULT 分区 ⇒ 不存在 750 踩过的「DEFAULT 越界行」约束校验，
    -- 可以直接 PARTITION OF。lock_timeout 沿用同事务设置（5s 由调用方钉）。
    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.ursm_node_snapshot_min
         FOR VALUES FROM (%L) TO (%L)',
        pname, start_ts, end_ts);
END;
$$;

-- 751 同款：函数级 GUC 钉扎，防 UTC 会话产出错位 8 小时的分区边界。
ALTER FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE)
    SET timezone = 'Asia/Shanghai';

COMMENT ON FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(DATE) IS
    '按日分区 ensure（830）。照 750_usage_facts_daily_partition 范式 + 751 时区钉扎。
由 db.go boot ensure 与 bg/partition_manager.go ensureSpecs 24h tick 双通道调用。
★ 830 是手工迁移，不在 installer 自动序列里 —— 该函数可能长期不存在，
  两个调用方都必须容忍 undefined_function（42883）。';

-- 1e) 预建当日 / 次日 / 后日。
--     ★ 建 3 天而非 750 的 2 天：本表不留 DEFAULT 兜底（硬约束 2），
--       多预建一天把「tick 恰好横跨一次失败」的空窗从 24h 压到 48h。
SELECT public.ensure_ursm_node_snapshot_min_daily_partition((now() AT TIME ZONE 'Asia/Shanghai')::date);
SELECT public.ensure_ursm_node_snapshot_min_daily_partition(((now() AT TIME ZONE 'Asia/Shanghai')::date) + 1);
SELECT public.ensure_ursm_node_snapshot_min_daily_partition(((now() AT TIME ZONE 'Asia/Shanghai')::date) + 2);

COMMIT;

-- ===========================================================================
-- 步骤 2：验收（设计稿 §7 V1~V7）。全部为只读，可反复跑。
-- ===========================================================================
-- V1 旧表完整保留（期望 ≈ 1,871.9 万行 / ≈ 10 GB）
-- SELECT count(*) AS legacy_rows,
--        pg_size_pretty(pg_total_relation_size('public.ursm_node_snapshot_min_legacy')) AS legacy_size
--   FROM public.ursm_node_snapshot_min_legacy;
--
-- V2 新表确为分区父表（期望 relkind='p'、relispartition=false）
-- SELECT relkind, relispartition FROM pg_class
--  WHERE oid = 'public.ursm_node_snapshot_min'::regclass;
--
-- V3 三个分区已挂（期望 3 行，ursm_node_snapshot_min_YYYYMMDD）
-- SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
--  WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass ORDER BY 1;
--
-- V4 写入真的进来了（1~2 个周期即 60~120 秒内 > 0）
-- SELECT count(*) FROM public.ursm_node_snapshot_min;
--
-- V5 写入速率正常（期望增速 ≈ 1,253~1,259 行/分钟）
-- SELECT n_tup_ins, n_live_tup FROM pg_stat_user_tables
--  WHERE relname = 'ursm_node_snapshot_min';
--
-- V6 ★ 分区边界是上海日历（必须是 +08，不能是 Z）
-- SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
--   FROM pg_class c WHERE c.relname LIKE 'ursm_node_snapshot_min_2%' ORDER BY 1;
--
-- V7 _legacy 不再增长（连续 2 个周期 n_tup_ins 不变）
-- SELECT n_tup_ins, n_tup_upd, n_tup_del FROM pg_stat_user_tables
--  WHERE relname = 'ursm_node_snapshot_min_legacy';

-- ===========================================================================
-- 步骤 3：观察 ≥ 7 天后再 DROP 旧表（**本文件不执行，须人工单独跑**）
--   ★ 切换后 retention worker 打的是新表名，扫不到 _legacy ⇒ no-op，
--     那 10 GB 不会被自动清理。这是有意的保底可回滚设计，
--     但必须有人负责在第 7 天执行，否则 10 GB 白占。
--   ★ 前提：新表已稳定承接写入（V4/V5 连续正常 7 天）。
-- ===========================================================================
-- DROP TABLE public.ursm_node_snapshot_min_legacy;
