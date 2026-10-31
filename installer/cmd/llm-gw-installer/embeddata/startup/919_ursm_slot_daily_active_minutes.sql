-- ===========================================================================
-- File:          sql/migrations/startup/919_ursm_slot_daily_active_minutes.sql
-- Migration:     919
-- Database:      llm_gateway
-- Purpose:       P11 第二期 —— 给 ursm_snapshot_slot_daily 补上 **active_minutes**
--                （每桶内有数据的「分钟子单位」数），并把填充函数升级为同步产出它。
--                让巡检（ursm-snapshot-health.sh）的 base_active 基线也能走汇总表，
--                不再每小时扫 3 天源表。
--
-- ── 缺口本体（2026-10-29 R176 轮，252 生产只读实测，非推断）────────────────
-- 252 SQL 日志 26.4h 窗（10-28 03:37 → 10-29 06:03）：巡检主判据（WITH win 族）
-- 22 次 / 合计 5,339.5s / 均值 242.7s / max 569.3s，**每小时恰好一次**，
-- 是全窗第一大慢查族（占全部慢查时间 10.9%）。
--
-- 为什么 P11（迁移 892）落地之后还这么贵：892 只把 **base_buckets**（7 天行数基线）
-- 搬上了汇总表；**base_active**（活跃分钟基线，ACTIVE_DAYS=3 天）仍实时扫源表：
--
--   EXPLAIN (ANALYZE)（252，2026-10-29，独立臂）：
--     4 个日分区 Index Only Scan，actual rows = 1,992,870 + 2,536,736
--        + 2,813,857 + 666,427 ≈ **800 万索引项**
--     Heap Fetches ≈ 831k（可见性地图未置全）
--     Execution Time: **158,145.912 ms**
--
-- ★ 行量基线漂移是主因：巡检脚本注释里的实测「7 天 ≈ 600 万行」是 R97 时代
--   （10-24 前后）的读数；本轮实测 252 日分区 n_live_tup ≈ **286~314 万行/天**
--   （10-22~10-28 七个分区）⇒ **行量涨了 ~3.4 倍** ⇒ base_active 的 3 天扫描
--   从注释里的 9.7s 涨到 158s（×16），叠加算上 base_buckets 的「今天」腿与
--   cur 腿，主判据均值 242s。
--
--   ★★ 「每小时 158s × 24」= 每天约 1 小时的纯扫描，换来的只是 3~4 个小时位的
--   基线中位数 —— 而这批「过去天的 count + DISTINCT 分钟」与 892 给 base_buckets
--   物化的「过去天的 count」是**同一类不变量**（过去天不会变，只有迟到行）。
--   ⇒ ⇒ **把它也物化进同一张汇总表**，巡检的过去天读点归零。
--
-- ── 形态 ─────────────────────────────────────────────────────────────────
--   ① ALTER TABLE public.ursm_snapshot_slot_daily
--        ADD COLUMN IF NOT EXISTS active_minutes integer;
--      · hour 粒度：count(DISTINCT date_trunc('minute', snapshot_ts))，域 [1,60]
--      · minute 粒度：恒 1（每分钟桶只含一个分钟子单位 —— 与巡检脚本
--        G_ACTIVE 对 minute 粒度取 `1::bigint` 同一语义）
--   ② CREATE OR REPLACE FUNCTION refresh_ursm_snapshot_slot_daily：
--      · 函数签名不变（days_back, granularity），调用方零改动
--      · INSERT ... SELECT 增加 active_minutes 列
--      · ON CONFLICT DO UPDATE 同步更新 active_minutes（迟到行语义与 rows 一致）
--      · 保留窗口 / 保留期（day < v_to - 9）逻辑原样
--
-- ── 部署次序契约（★ 巡检脚本与本迁移的握手）───────────────────────────────
--   列加上之后、**下一次全量刷新跑完之前**，存量行的 active_minutes 全是 NULL。
--   巡检脚本（同轮改造）会先探测「窗口内 active_minutes 非空的行数」，
--   探测不过 ⇒ 自动回退到 892 时代的老路（过去天也扫源表）⇒ **慢，但不失明**
--   （本目录既有哲学）。⇒ 本迁移落地后应当尽快手工触发一次全量回填：
--       SELECT public.refresh_ursm_snapshot_slot_daily(8, 'hour');
--   （★ 不要放进本迁移文件：全量刷新按 2026-10-29 行量实测需 10 分钟以上，
--     迁移通道的 statement_timeout 承不住 ⇒ 台账会在回滚后留下半截状态。
--     刷新函数幂等，手工/重复触发都安全。）
--
-- ── 锁与预算 ─────────────────────────────────────────────────────────────
--   · ADD COLUMN（无 DEFAULT）只是元数据变更，瞬间完成；但槽表极小且常被
--     刷新函数整事务持有 ⇒ 挂 lock_timeout 快失败，而不是排在刷新事务后面。
--   · ★ 会话级 SET，不是 SET LOCAL（914/916 文件头实测：本通道 `psql -f`
--     逐文件执行、不在事务块内，SET LOCAL 只打 WARNING 后完全失效）。
--   · 本文件不含 CONCURRENTLY ⇒ installer 默认 --single-transaction 通道可用。
-- ===========================================================================

SET lock_timeout = '2s';

-- ── ① 新列 ───────────────────────────────────────────────────────────────
ALTER TABLE public.ursm_snapshot_slot_daily
    ADD COLUMN IF NOT EXISTS active_minutes integer;

COMMENT ON COLUMN public.ursm_snapshot_slot_daily.active_minutes IS
    '该 (天, 粒度, 小时位, 桶) 内有数据的「分钟子单位」数（hour: 1..60；minute: 恒 1）。'
    '巡检 base_active 基线（SPARSE/SLOW 分型判据）的物化输入。';

-- ── ② 填充函数升级（签名不变；增量 = active_minutes 的计算与更新）─────────
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
    v_active text;
BEGIN
    IF p_days_back IS NULL OR p_days_back < 1 THEN
        RAISE EXCEPTION 'refresh_ursm_snapshot_slot_daily: p_days_back must be >= 1, got %', p_days_back;
    END IF;

    IF p_granularity IS NULL OR p_granularity NOT IN ('hour', 'minute') THEN
        RAISE EXCEPTION 'refresh_ursm_snapshot_slot_daily: p_granularity must be hour|minute, got %', p_granularity;
    END IF;

    -- ★ 两种粒度的 slot / bucket / active 表达式：
    --   hour   => slot = 小时位 0..23，bucket = 小时对齐，
    --             active = 该桶内有数据的分钟数（count(DISTINCT minute)）
    --   minute => slot = 分钟位 0..1439，bucket = 分钟对齐，
    --             active = 恒 1（一个分钟桶只含一个分钟子单位）
    -- ★ 巡检脚本的 G_SLOT_TS / G_TRUNC / G_ACTIVE 与此【必须逐字一致】
    --   （巡检侧 hour 粒度的 G_ACTIVE 就是本式的别样写法，见 917 门）。
    IF p_granularity = 'hour' THEN
        v_slot   := 'extract(hour from date_trunc(''hour'', snapshot_ts))::int';
        v_bucket := 'date_trunc(''hour'', snapshot_ts)';
        v_active := 'count(DISTINCT date_trunc(''minute'', s.snapshot_ts))';
    ELSE
        v_slot   := '(extract(hour from snapshot_ts)::int * 60 + extract(minute from snapshot_ts)::int)';
        v_bucket := 'date_trunc(''minute'', snapshot_ts)';
        v_active := '1';
    END IF;

    v_to := (now() AT TIME ZONE current_setting('TimeZone'))::date;
    v_from := v_to - p_days_back;

    -- ★ 源表不存在时**静默返回 0**（892 原语义）：新装库可能还没有源表。
    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RETURN 0;
    END IF;

    -- ★ EXECUTE + format：粒度相关表达式只能是本函数内写死的常量（无注入面）。
    --   ★★ active_minutes 必须随 ON CONFLICT DO UPDATE 一并更新：
    --     迟到行既会改 rows 也会改 active_minutes（某分钟的首行迟到），
    --     只更新 rows 会留下「行数新、分钟数旧」的半截基线 ⇒ 判据分型失真。
    EXECUTE format(
        'INSERT INTO public.ursm_snapshot_slot_daily AS d (day, granularity, slot, bucket_ts, rows, active_minutes)
         SELECT (s.snapshot_ts AT TIME ZONE current_setting(''TimeZone''))::date AS day,
                %L AS granularity,
                %s AS slot,
                %s AS bucket_ts,
                count(*) AS c,
                %s AS active_minutes
           FROM public.ursm_node_snapshot_min s
          WHERE s.snapshot_ts >= $1::timestamptz
            AND s.snapshot_ts <  $2::timestamptz
          GROUP BY 1, 2, 3, 4
         ON CONFLICT (day, granularity, slot, bucket_ts) DO UPDATE
            SET rows = EXCLUDED.rows,
                active_minutes = EXCLUDED.active_minutes',
        p_granularity, v_slot, v_bucket, v_active)
    USING v_from::timestamptz, v_to::timestamptz;

    GET DIAGNOSTICS v_rows = ROW_COUNT;

    -- ★ 只保留最近 N 天（892 原语义：基线窗口 7 天 + 2 天余量）。
    DELETE FROM public.ursm_snapshot_slot_daily d
     WHERE d.day < v_to - 9;

    RETURN v_rows;
END;
$fn$;
