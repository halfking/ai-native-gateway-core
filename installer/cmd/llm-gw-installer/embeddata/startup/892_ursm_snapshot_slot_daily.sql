-- 892_ursm_snapshot_slot_daily.sql
-- 2026-10-20：给 URSM 快照健康巡检建**每日 slot 汇总表** —— P11 的落点。
--
-- -----------------------------------------------------------------------
-- 缺陷本体：巡检每小时重扫 7 天基线（§143.29 成本 #1）
--
-- scripts/252-monitor/ursm-snapshot-health.sh 的 base_buckets：
--     SELECT $G_SLOT_TS AS slot, count(*) AS c
--       FROM ursm_node_snapshot_min
--      WHERE snapshot_ts >= $NOW_EXPR - make_interval(days => $BASELINE_DAYS)
--        AND snapshot_ts < (SELECT lo FROM win)
--      GROUP BY 1, $G_TRUNC snapshot_ts
--
-- 实测（252，pg_stat_statements，41 h 窗口）：
--     shared_blks_read = 374,496 块/次 = 2.9 GB/次
--     mean_exec_time  = 190,473 ms（190 s）
--     max_exec_time   = 591,621 ms（592 s）
--     26 次成功 ⇒ 累计 76,069.6 MB（74 GB）
-- ⇒ §85 的成本分解：base_buckets 占 61%、base_active 35%、cur 4%
--
-- -----------------------------------------------------------------------
-- 为什么这个形态对（★ 论证已在 §143.35 被【实测修正】，勿再引用旧判词）
--
-- ★ base_buckets 的产出只有两个用途：
--   ① 按小时位 slot 分组，每组取【中位数】（与同时段基线比大小）；
--   ② 输出 c（行数基线）。
--
-- ★ 这两个用途都只依赖「每个 (天, 小时位, 桶) 的行数」这一个量 ——
-- ★ 而这个量在【已经过去的一天之内】是不变的（只在跨天时新增一批）
-- ⇒ ⇒ 把「(天, slot, 桶) → 行数」每天算一次存起来，
--   巡检时用最近 7 天的这张表算中位数。
--
-- ⚠⚠ **订正（§143.35 实测，2026-10-20）**：这个论证**原本被写成「零语义风险」**，
-- ⚠⚠ **而它【不是】零的** —— 第一次 A/B 实测 FAIL（16 行 OLD_ONLY，全是今天的桶）：
--   ★ **填充范围是 [today - N, today)、【不含今天】**（今天的数据还在写）
--   ⇒ 而源表路读 now - 7 days 起 ⇒ **含今天**
--   ⇒ ⇒ **修正 = 方案 B**：**过去的完整天走汇总表 + 今天仍实时读源表**
--   （★ 今天的数据在变，本来就该实时读；「不变的那部分」才是可以物化的）。
-- ⚠ **另一处【判定输入的真实改动】**：窗口下界从「行时间」改成「桶对齐」
--   （date_trunc 边界桶从半截变整桶）⇒ ★★ **这不是零成本改动 ⇒ 需属主知情拍板**。
--
-- ⇒ ⇒ ★ 与「缩短 7 天窗」不同：「缩短窗口」= 改判据的语义；
--   ★「换个地方算」= 改实现 ⇒ 但**前提是它真的只换实现** ⇒
--   ⇒ ⇒ ★★★ **这个前提必须实测，不能只论证**（§143.35 的三层断言：
--   ab_identical / p50_identical / **verdict_identical**，本地真库全 PASS）。
-- ⇒ ⇒ ⚠ 而「省多少」仍然是【估】（−96% 扫描量 / −85% 物理读未在生产实测）
--   ⇒ ⇒ ★ **不因等价性已证就承诺收益数字**。
--
-- -----------------------------------------------------------------------
-- 表形态
--
--   ursm_snapshot_slot_daily(
--       day        date        NOT NULL,   -- 该行所属的自然日（会话时区）
--       granularity text       NOT NULL,   -- 'hour' | 'minute'（两种 bucket 对齐不同）
--       slot       int         NOT NULL,   -- hour: 0..23 / minute: 0..1439
--       bucket_ts  timestamptz NOT NULL,   -- 该桶的起点（按粒度对齐）
--       rows       bigint      NOT NULL,   -- 该 (天, 粒度, 小时位, 桶) 的行数
--       PRIMARY KEY (day, granularity, slot, bucket_ts)
--   )
--
-- ★ 为什么主键带 day：同一个小时位在不同天的桶起点不同 ⇒ 必须带 day 才唯一。
-- ★ 为什么带 granularity：hour 粒度的 bucket 是小时对齐、minute 粒度是分钟对齐
--   ⇒ 两种的 bucket_ts 语义不同 ⇒ 混在一起会读出错的桶。
--
-- ★ 体积：小时粒度 24 slot × 24 桶 = 576 行/天；分钟粒度 34,560 行/天。
--   ★ 7 天窗口只需几千行 ⇒ 巡检侧从「每小时扫 2,600 万行」变成「读几千行」。
--
-- ★ 与 830 的关系：830（`.skip`，手工执行）把 ursm_node_snapshot_min 变成分区表。
--   本迁移【不依赖】830 —— 它读的是表本身，与表是否分区无关。
--   ⇒ ★ 两个迁移可以独立授权、独立生效。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '300s';

-- -----------------------------------------------------------------------
-- 表
-- -----------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.ursm_snapshot_slot_daily (
    day         date        NOT NULL,
    granularity text        NOT NULL,
    slot        int         NOT NULL,
    bucket_ts   timestamptz NOT NULL,
    rows        bigint      NOT NULL,
    CONSTRAINT ursm_snapshot_slot_daily_pkey PRIMARY KEY (day, granularity, slot, bucket_ts)
);

COMMENT ON TABLE public.ursm_snapshot_slot_daily IS
    'URSM 快照健康巡检的每日 slot 基线汇总（P11，迁移 892）。'
    '每行 = (自然日, 粒度, 小时位, 桶起点) → 行数。'
    '由 refresh_ursm_snapshot_slot_daily() 幂等填充；'
    '巡检脚本 ursm-snapshot-health.sh 的 base_buckets 改读本表。';

COMMENT ON COLUMN public.ursm_snapshot_slot_daily.day IS
    '该桶所属的自然日（按会话时区截断）。';
COMMENT ON COLUMN public.ursm_snapshot_slot_daily.granularity IS
    '统计粒度：hour（小时位 0..23）或 minute（分钟位 0..1439）。'
    '★ 两种粒度的 bucket_ts 语义不同（小时对齐 vs 分钟对齐）⇒ 必须带本列区分。';
COMMENT ON COLUMN public.ursm_snapshot_slot_daily.slot IS
    '小时位（hour: 0..23；minute: 0..1439）；巡检侧的唯一读取键。';
COMMENT ON COLUMN public.ursm_snapshot_slot_daily.bucket_ts IS
    '该桶的起点（按 granularity 对齐）。';
COMMENT ON COLUMN public.ursm_snapshot_slot_daily.rows IS
    '该 (天, 粒度, 小时位, 桶) 在源表里的行数。';

-- -----------------------------------------------------------------------
-- 填充函数（幂等）
--
-- ★★ 形态选择：把「怎么算」放进【SQL 函数】，而不是放进巡检脚本 ——
--   ① 巡检脚本是【纯只读】的（§143.14 起它的性质就是只读巡检）
--      ⇒ ★ 往里塞 INSERT 会破坏它的性质，也会让「巡检能不能写」这个
--        问题重新变成一个待查项；
--   ② 函数是【幂等】的 ⇒ 任何调用方（Go 后台作业 / cron / 手工）都能安全重跑；
--   ③ 判定输入逐字不变 ⇒ 迁移不碰判据。
--
-- ★ p_days_back = 1（默认）⇒ 只补【昨天】。
--   为什么只补昨天：
--   ★ 「(天, slot, 桶) → 行数」在**当天之内是不变的** ——
--     只有跨天时才会新增一批（快照按天分批写入）。
--   ⇒ ⇒ 每天补一次昨天就够 ⇒ 每天扫 1 天的数据，而不是 7 天。
--   ⇒ ⇒ 成本：3.75M 行/天扫一次（估，未实测），
--     而不是 26M 行 × 24 次/天。
--
-- ★ 用 ON CONFLICT DO UPDATE 而不是 DO NOTHING：
--   昨天的桶在「补写」的时刻可能还没写完（当天仍有迟到数据）
--   ⇒ 重跑一次要把它的行数【更新】到最新值 ⇒ DO NOTHING 会留下过时的行数。
--   ⇒ ⇒ ★ 这让函数**可反复重跑**且结果收敛到正确值。
--
-- ★★ v_to 的取法（本地真库实测踩过的坑）：
--   v_to := 今日（按会话时区）。而 WHERE 上界是 `v_to::timestamptz`
--   = 今日 00:00 ⇒ **今日的数据永远不在填充范围内**（今日还在写）。
--   ⇒ ⇒ 所以「p_days_back = 1」实际覆盖的是【昨天一天】，
--     而**不是**「昨天 + 今天」⇒ ⇒ ★★ **这一点必须写进调用方的约定里**。
-- -----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.refresh_ursm_snapshot_slot_daily(
    p_days_back integer DEFAULT 1,
    p_granularity text DEFAULT 'hour'
) RETURNS bigint
LANGUAGE plpgsql
AS $fn$
DECLARE
    v_rows   bigint := 0;
    v_from   date;
    v_to     date;
    v_slot   text;
    v_bucket text;
BEGIN
    IF p_days_back IS NULL OR p_days_back < 1 THEN
        RAISE EXCEPTION 'refresh_ursm_snapshot_slot_daily: p_days_back must be >= 1, got %', p_days_back;
    END IF;

    IF p_granularity IS NULL OR p_granularity NOT IN ('hour', 'minute') THEN
        RAISE EXCEPTION 'refresh_ursm_snapshot_slot_daily: p_granularity must be hour|minute, got %', p_granularity;
    END IF;

    -- ★ 两种粒度的 slot / bucket 表达式不同：
    --   hour   => slot = 小时位 0..23，bucket = 小时对齐
    --   minute => slot = 分钟位 0..1439，bucket = 分钟对齐
    -- ★ 巡检脚本的 G_SLOT_TS / G_TRUNC 与此【必须逐字一致】。
    IF p_granularity = 'hour' THEN
        v_slot   := 'extract(hour from date_trunc(''hour'', snapshot_ts))::int';
        v_bucket := 'date_trunc(''hour'', snapshot_ts)';
    ELSE
        v_slot   := '(extract(hour from snapshot_ts)::int * 60 + extract(minute from snapshot_ts)::int)';
        v_bucket := 'date_trunc(''minute'', snapshot_ts)';
    END IF;

    v_to := (now() AT TIME ZONE current_setting('TimeZone'))::date;
    v_from := v_to - p_days_back;

    -- ★ 源表不存在时**静默返回 0**而不是报错：
    --   211 的 ursm_node_snapshot_min 是普通表、也存在；
    --   但新装库可能还没有这张表 ⇒ 填充作业不该因此让整个后台作业失败。
    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RETURN 0;
    END IF;

    -- ★ 用 EXECUTE + format：slot/bucket 表达式要随粒度切换，
    --   而 PL/pgSQL 的 EXECUTE 不接受标识符参数 ⇒ 只能拼 SQL 文本。
    --   ★ 拼接进去的只有两个【本函数内写死的常量表达式】（不是用户输入）⇒ 无注入面。
    EXECUTE format(
        'INSERT INTO public.ursm_snapshot_slot_daily AS d (day, granularity, slot, bucket_ts, rows)
         SELECT (s.snapshot_ts AT TIME ZONE current_setting(''TimeZone''))::date AS day,
                %L AS granularity,
                %s AS slot,
                %s AS bucket_ts,
                count(*) AS c
           FROM public.ursm_node_snapshot_min s
          WHERE s.snapshot_ts >= $1::timestamptz
            AND s.snapshot_ts <  $2::timestamptz
          GROUP BY 1, 2, 3, 4
         ON CONFLICT (day, granularity, slot, bucket_ts) DO UPDATE
            SET rows = EXCLUDED.rows',
        p_granularity, v_slot, v_bucket)
    USING v_from::timestamptz, v_to::timestamptz;

    GET DIAGNOSTICS v_rows = ROW_COUNT;

    -- ★ 只保留最近 N 天 ⇒ 这张表本身不会单调增长。
    --   保留期 = 巡检的基线窗口（7 天）+ 2 天余量。
    --   ★ 删掉超出保留期的行**不影响任何判定**（巡检只读最近 7 天）。
    DELETE FROM public.ursm_snapshot_slot_daily d
     WHERE d.day < v_to - 9;

    RETURN v_rows;
END;
$fn$;

COMMENT ON FUNCTION public.refresh_ursm_snapshot_slot_daily(integer, text) IS
    '幂等填充 ursm_snapshot_slot_daily（默认补昨天、hour 粒度）。返回受影响行数。'
    '可反复重跑且结果收敛。迁移 892（P11）。'
    '★ 注意：填充范围是 [today - p_days_back, today)，【不含今天】。'
    '★ 两种粒度的 slot/bucket 表达式必须与巡检脚本的 G_SLOT_TS/G_TRUNC 逐字一致。';

-- -----------------------------------------------------------------------
-- ★★ 【不要】再为「单参数」建一个兼容重载 —— 本地真库实测证明它有害。
--
-- ★ PG 的重载判定看的是「参数类型 + 哪些参数可省略」：
--   只要存在一个【可省略参数】的重载，`f(1)` 就会同时匹配
--   `(integer)` 与 `(integer, text)`（后者省略第 2 参）⇒
--   **ERROR: function ... is not unique**（本地 PG 17.10 实测）。
--   ⇒ ⇒ ★★ 而「把单参数版的默认值去掉」也【救不了】—— 实测同样报 not unique。
--   ⇒ ⇒ ★★ **唯一可行形态：只保留【一个】函数**，第二个参数带默认值。
--   ⇒ ⇒ ★ 好处不变：`f(1)` 仍能工作（落到 'hour' 默认值），
--   ★ 且需要 minute 时显式传 `f(8, 'minute')`。
-- -----------------------------------------------------------------------

-- -----------------------------------------------------------------------
-- ★★★ 首次回填（迁移内做，不靠调用方记得做）
--
-- ★★ 为什么必须在这里做：
--   巡检的基线窗是 **7 天**。若表是空的，巡检读到的是「没有基线」
--   ⇒ ★★ **所有桶都判成 NO-BASELINE ⇒ 巡检会安静地不再告警**
--   ⇒ ⇒ ★★★ **那比「慢」更坏**：巡检从「慢」变成「失明」，
--     而外部完全看不出来（脚本仍然每次都成功退出）。
--   ⇒ ⇒ ★ 所以「表可用」是【部署的前置条件】，不是「以后慢慢填」。
--
-- ★ 回填 8 天（7 天基线窗 + 1 天余量）⇒ 表里立刻有完整的 7 天基线。
--   ★ 幂等：重跑本迁移不会破坏（CREATE TABLE IF NOT EXISTS + 函数幂等 + 回填幂等）。
--
-- -----------------------------------------------------------------------
-- ★★★ 【回填必须抬高 statement_timeout】—— 这是本迁移在生产上能否活下来的
--      决定性一行（2026-10-22 R75 轮实测发现，P11-R75）。
--
-- ★ 为什么把预算抬到 30min（★ **本条曾被本轮自己的实测推翻过一次**，见下）
--
--   ⚠⚠⚠ **订正（2026-10-22 R75 轮，生产实测）**：
--     本文件原注释断言「300 s **必然超时** ⇒ 整条迁移回滚」。
--     ★ **这句话是错的** —— 生产 252 上把**本回填的同形态查询**逐条实测：
--         1 天窗口 · hour 粒度  = **14,696 ms**
--         8 天窗口 · hour 粒度  = **70,136 ms**   ← 回填的 hour 那一半
--         8 天窗口 · minute 粒度 = **79,335 ms**   ← 回填的 minute 那一半
--         ⇒ **两次合计 ≈ 149 s** ⇒ ★ **落在原 300 s 预算之内**。
--     ⇒ ⇒ ★★ 所以「缺陷①：892 必然超时」**不成立** ⇒ 本迁移**原样就能落**。
--     ⇒ ⇒ ★★ **那为什么还要抬高？** ⇒ 因为 149 s 这个读数是【一次】的、
--        且是在**当前负载**下测的；而同族查询在生产上曾观测到 **max 591,621 ms**
--        （巡检的 base_buckets，比回填多带子查询 + DISTINCT + 负载）⇒
--        ⇒ ★ **2× 余量在共享忙库上偏薄** ⇒ ⇒ **抬到 30min 是【余量保险】，不是修 bug**。
--     ⇒ ⇒ ★★ **代价必须写清楚**：本文件字节一变 ⇒ 通道的**内容指纹**判定为
--        「content replay」⇒ **已应用过 892 的库会重跑一次回填**
--        （本地 1.1M 行 = 2.4 s，无所谓；大表要再付一次 8 天扫描）。
--
--   ★ 原始推理链（保留，因为它推出了正确的**机制**，只是**推论**错了）：
--     源表 ursm_node_snapshot_min = 30,311,927 行、跨度 2026-10-15 ~ 2026-10-22
--     （9 个【日】分区 ⇒ 8 天窗口 ≈ 覆盖整张表）
--     而 pg_stat_statements 里同形态的 7 天 GROUP BY 读数是 mean 235,016 / max 591,621 ms
--     ⇒ ⇒ 【DO 块里两次回填共享同一个语句预算】⇒ ⇒ 预算必须按【块】算。
--
-- ★★★ 为什么【块内】抬高无效、必须在 DO 【之前】抬（本地 PG 17.10 实测）：
--     · 实验 A：DO 块内两条语句各 sleep 1.5s、块级超时 2s ⇒ 【第二条被取消】
--       ⇒ DO 块内的多条语句【共享同一个语句预算】⇒ 嵌套语句不重置计时器。
--     · 实验 B：DO 块【内部】 SET LOCAL statement_timeout='30s' ⇒ 【仍被取消】
--       ⇒ 外层计时器已武装，块内改 GUC 不影响已在跑的语句。
--     · 实验 C：在 DO 块【之前】 SET LOCAL statement_timeout='30s' ⇒ 【通过】
--       ⇒ 计时器在语句启动时按当时的 GUC 值武装 ⇒ 抬高必须发生在那之前。
--     · 实验 D：拆成两个独立 DO 块 ⇒ 各自有独立预算（另一条可行修法）。
--   ⇒ 本迁移采用实验 C 的形态：把抬高写在 DO 之前。
--
-- ★ 30min 的依据：上限 ~1,180 s（两次回填的 max 之和）⇒ 取 30min 留足余量。
--   ★ 这是本迁移在生产上**唯一**的重量级步骤（一次性 ~3 GB 物理读），
--     之后由每日刷新作业增量维护（每次只扫 1~2 天）。
--   ⇒ ⇒ ★ **落生产需要属主批一个窗口**（见 P11-R75 的授权材料）。
-- -----------------------------------------------------------------------
SET LOCAL statement_timeout = '30min';

DO $backfill$
DECLARE
    v_inserted bigint;
BEGIN
    -- ★ 源表不存在 ⇒ 跳过回填（不报错）：新装库还没有这张表。
    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RAISE NOTICE '892: source table public.ursm_node_snapshot_min absent — backfill skipped';
        RETURN;
    END IF;

    -- ★ 填【两种粒度】：hour（生产在用）与 minute（脚本支持但线上未用）。
    --   两种的 bucket 对齐方式不同 ⇒ 混在一起会读出错的桶 ⇒ 必须分开存。
    SELECT public.refresh_ursm_snapshot_slot_daily(8, 'hour') INTO v_inserted;
    RAISE NOTICE '892: backfilled % rows (hour, 8 days) into ursm_snapshot_slot_daily', v_inserted;

    SELECT public.refresh_ursm_snapshot_slot_daily(8, 'minute') INTO v_inserted;
    RAISE NOTICE '892: backfilled % rows (minute, 8 days) into ursm_snapshot_slot_daily', v_inserted;

    -- ★ 自检：回填后表必须【非空】⇒ 否则巡检会失明。
    IF NOT EXISTS (SELECT 1 FROM public.ursm_snapshot_slot_daily) THEN
        RAISE WARNING '892: backfill produced ZERO rows — the patrol would have NO baseline';
    END IF;
END;
$backfill$;

COMMIT;
