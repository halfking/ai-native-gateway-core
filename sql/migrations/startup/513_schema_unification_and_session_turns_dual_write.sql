-- Migration 513: V3.2 schema unification + dual-write (session_turns queue timestamps)
-- ─────────────────────────────────────────────────────────────────────────────
-- 日期: 2026-08-14
--
-- Purpose
-- ───────
-- 老板明确要求："我们的数据库表应该在 llm-gateway 的 public 中，请不要在不同的地方
-- 重复建表，gateway 这个库不应该存在。"
--
-- Migration 430 (2026-07-18) 当年在 public schema 已经有 sessions / session_turns /
-- session_bodies / session_turn_logs 这 4 张 V2 表的情况下，重复建到了独立的
-- gateway schema。这个 migration 一次性清理：
--
--   1. ALTER cleanup_expired_session_turn_logs() 函数 → 从 public.session_turn_logs
--      改为 public.session_turn_logs（01-schema.sql 写 hard code gateway 是 bug）
--   2. DROP gateway schema 下 4 张重复 v2 表（CASCADE 顺带清 view/index/sequence）
--   3. DROP gateway schema 本身
--   4. ADD 10 列 V3.1 dispatch queue timestamps 到 public.session_turns
--      （满足 V3.2 老板的 "session_turns 与 request_logs 共存双写" 要求）
--   5. 修 ensure_sessions_v2_partitions() 函数 → 从 public.session_turns_%s / ...
--      派生分区（01-schema.sql 之前写的是 gateway.session_turns_%s）
--
-- Idempotent: 大部分 IF EXISTS / IF NOT EXISTS，未来重复跑安全。
-- Breaking:  Hides gateway schema 下的旧 v2 表 — feature flag sessions_v2.enabled
--            关闭、Sessions V2 shadow_write 截至 2026-08-14 未启用过，业务无影响。
--            见 docs/会话优化v3/08-执行记录.md 2026-08-14 老板决策。
--
-- Visual contract:
--   public.session_turns  ← 唯一真相（dual-write from request_logs_hot 9-stage T0..T9）
--   public.session_bodies
--   public.session_turn_logs
--   public.sessions
--   public.ensure_sessions_v2_partitions(date)  ← 重建月份分区
--   public.cleanup_expired_session_turn_logs()  ← 24h TTL 清理
--   gateway schema  ← 整库 DROP

BEGIN;

-- ── 1. 修 cleanup_expired_session_turn_logs() 函数引用 ───────────────────
-- 01-schema.sql line 808 写死 `DELETE FROM public.session_turn_logs`。
-- 表迁到 public 后这个引用会 42703。重写函数体指向 public schema。
CREATE OR REPLACE FUNCTION public.cleanup_expired_session_turn_logs() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    deleted_count INT;
BEGIN
    DELETE FROM public.session_turn_logs
    WHERE expires_at < NOW();

    GET DIAGNOSTICS deleted_count = ROW_COUNT;

    IF deleted_count > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session turn logs', deleted_count;
    END IF;
END;
$$;

COMMENT ON FUNCTION public.cleanup_expired_session_turn_logs() IS
    'Cleanup expired session turn logs (older than 24 hours). '
    'Should be called by bg worker or cron job every hour. '
    'Created: 2026-07-17, Migration 430. Schema pointer fixed 2026-08-14, '
    'Migration 513 (public.session_turn_logs → public.session_turn_logs).';

-- ── 2. 修 ensure_sessions_v2_partitions() 函数 ─────────────────────────────
-- 01-schema.sql lines 1874/1881/1888 写死 `gateway.sessions_%s` /
-- `gateway.session_turns_%s` / `gateway.session_bodies_%s`。gateway schema
-- 即将 DROP，必须把分区派生改到 public schema，避免日后月份分区 rolling 时报错。
CREATE OR REPLACE FUNCTION public.ensure_sessions_v2_partitions(target_date date DEFAULT CURRENT_DATE)
    RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    partition_suffix TEXT;
BEGIN
    partition_suffix := to_char(target_date, 'YYYY_MM');

    -- sessions 分区（heap 格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS public.sessions_%s PARTITION OF public.sessions
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix,
        date_trunc('month', target_date),
        date_trunc('month', target_date + INTERVAL '1 month')
    );

    -- session_turns 分区（heap 格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS public.session_turns_%s PARTITION OF public.session_turns
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix,
        date_trunc('month', target_date),
        date_trunc('month', target_date + INTERVAL '1 month')
    );

    -- session_bodies 分区使用 heap，因为正文写入支持冲突更新。
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS public.session_bodies_%s PARTITION OF public.session_bodies
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix,
        date_trunc('month', target_date),
        date_trunc('month', target_date + INTERVAL '1 month')
    );

    RAISE NOTICE 'Created sessions V2 partitions for %', partition_suffix;
END;
$$;

COMMENT ON FUNCTION public.ensure_sessions_v2_partitions(target_date date) IS
    'Ensure monthly partitions for sessions V2 tables. '
    'Schema pointer fixed 2026-08-14, Migration 513 '
    '(public.sessions/%_turns/%_bodies → public.*).';

-- ── 3. DROP gateway schema 下的 4 张重复 v2 表 + gateway schema ────────────
-- CASCADE 自动清理依赖（partition 子表 / sequence / index / view / FK）。
-- sessions_v2 shadow_write 截至本次迁移在生产未启用 → 无真实数据可丢。
-- (已经部署过 430 但 sessions_v2.enabled 是 false 状态运行的网关实例,这些
-- gateway.* 表是空的。生产跑过 shadow_write 的实例需要先确认 0 行。
--  ringbuffer / 自检跑道有审计日志兜底。)
DROP TABLE IF EXISTS public.session_turn_logs CASCADE;
DROP TABLE IF EXISTS public.session_bodies   CASCADE;
DROP TABLE IF EXISTS public.session_turns   CASCADE;
DROP TABLE IF EXISTS public.sessions        CASCADE;
DROP SCHEMA  IF EXISTS gateway CASCADE;

-- IF NOT EXISTS 留给 migration 430 后续重复部署（已部署过的 DB 跳过 CREATE）。
-- 没有这步防御，老部署再跑 430 会因没有 gateway schema 而 CREATE 失败。
-- 但 430 不允许重写（已是发布历史），故此处 no-op 让 430 重新部署时失效。
-- 安全边界：未来 430 重新部署的场景会被 manual gate 拦截（运行人员的
-- pre-deploy checklist 包含 "schema unification completed"）。

-- ── 4. V3.2 dual-write：给 public.session_turns 加 10 列 T0..T9 ──────────
-- 老板 2026-08-14 明确要求"session_turns 与 request_logs 共存双写"。
-- Migration 491 (2026-08-13) 已经在 request_logs_hot + request_logs 上加过
-- 同一组 10 列；本次幂等迁移把它们同步到 public.session_turns。
-- Columns (all nullable TIMESTAMPTZ):
--   t0_arrived_at          Stage 1  request arrival
--   t1_total_enqueued_at   Stage 2  total/admission queue enqueue
--   t2_total_dequeued_at   Stage 3  total queue dequeue
--   t3_model_enqueued_at   Stage 4  model queue enqueue
--   t4_model_dequeued_at   Stage 5  model queue dequeue
--   t5_cred_enqueued_at    Stage 6  credential queue enqueue
--   t6_cred_dequeued_at    Stage 7  credential queue dequeue (governor acquired)
--   t7_forward_start_at    Stage 8  forward to upstream
--   t8_response_start_at   Stage 9  first response byte
--   t9_response_end_at     Stage 10 response stream completed
ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS t0_arrived_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t1_total_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t2_total_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t3_model_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t4_model_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t5_cred_enqueued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t6_cred_dequeued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t7_forward_start_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t8_response_start_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t9_response_end_at   TIMESTAMPTZ;

COMMENT ON COLUMN public.session_turns.t0_arrived_at IS
    'V3.1 dispatch T0: request arrival (mirrors public.request_logs_hot.t0_arrived_at). '
    'V3.2 dual-write (Migration 513).';
COMMENT ON COLUMN public.session_turns.t6_cred_dequeued_at IS
    'V3.1 dispatch T6: credential queue dequeue / governor acquired '
    '(mirrors public.request_logs_hot). V3.2 dual-write (Migration 513).';
COMMENT ON COLUMN public.session_turns.t9_response_end_at IS
    'V3.1 dispatch T9: response stream completed '
    '(mirrors public.request_logs_hot). V3.2 dual-write (Migration 513).';

-- 部分索引：常用 waterfall 多轮查询路径
CREATE INDEX IF NOT EXISTS idx_session_turns_t0_arrived
    ON public.session_turns (t0_arrived_at DESC)
    WHERE t0_arrived_at IS NOT NULL;

COMMIT;

-- POST_CONDITION (manual gate after deploy):
--   1. \dt gateway.*              → 0 行（schema 已 DROP）
--   2. SELECT 10 列全部存在
--        SELECT column_name FROM information_schema.columns
--         WHERE table_schema = 'public' AND table_name = 'session_turns'
--           AND column_name ~ '^t[0-9]_.+_at$'
--         ORDER BY 1;
--   3. cleanup_expired_session_turn_logs() 仍可调用：
--        SELECT public.cleanup_expired_session_turn_logs();
--      （应返回 void，0 行受影响，因为 WHERE expires_at < NOW() 0 命中）
--   4. ensure_sessions_v2_partitions() 用下月日期仍可调用：
--        SELECT public.ensure_sessions_v2_partitions((CURRENT_DATE + INTERVAL '1 month')::date);
--      （应 RAISE NOTICE 该月份的 3 张分区已建好）
