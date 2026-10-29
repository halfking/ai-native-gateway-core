-- ===========================================================================
-- File:          sql/migrations/startup/919_ursm_slot_daily_active_minutes.down.sql
-- Migration:     919 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 917 —— 删除 active_minutes 列并把填充函数还原为 892 原体
--                （只算 rows）。
--
-- ── 回滚后的行为 ────────────────────────────────────────────────────────
-- 巡检脚本（同轮改造）的 active 探测会因「列不存在 / 值为 NULL」探测失败，
-- 自动回退到 892 时代的老路（base_active 实时扫 3 天源表）⇒ 慢，但判定正确。
-- ⇒ ★ 回滚本身不产生「巡检失明」窗口（这正是探针存在的意义）。
--
-- ── 锁守卫（★ 会话级 SET，不是 SET LOCAL；与 up 文件同款理由）────────────
-- ★ 本文件**不**包 BEGIN/COMMIT：installer 的 applySQL 默认给每个文件加
--   --single-transaction ⇒ 显式 BEGIN 会变成嵌套事务 ⇒ 直接报错。
-- ===========================================================================

SET lock_timeout = '2s';

-- ── 函数还原为 892 原体（逐字，只算 rows）────────────────────────────────
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

    IF p_granularity = 'hour' THEN
        v_slot   := 'extract(hour from date_trunc(''hour'', snapshot_ts))::int';
        v_bucket := 'date_trunc(''hour'', snapshot_ts)';
    ELSE
        v_slot   := '(extract(hour from snapshot_ts)::int * 60 + extract(minute from snapshot_ts)::int)';
        v_bucket := 'date_trunc(''minute'', snapshot_ts)';
    END IF;

    v_to := (now() AT TIME ZONE current_setting('TimeZone'))::date;
    v_from := v_to - p_days_back;

    IF to_regclass('public.ursm_node_snapshot_min') IS NULL THEN
        RETURN 0;
    END IF;

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

    DELETE FROM public.ursm_snapshot_slot_daily d
     WHERE d.day < v_to - 9;

    RETURN v_rows;
END;
$fn$;

-- ── 列删除（最后做：先还原函数，刷新期间不再写该列）─────────────────────
ALTER TABLE public.ursm_snapshot_slot_daily
    DROP COLUMN IF EXISTS active_minutes;
