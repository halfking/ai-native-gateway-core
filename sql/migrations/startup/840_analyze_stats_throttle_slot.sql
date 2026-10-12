-- 840_analyze_stats_throttle_slot.sql
-- 2026-10-07: 给 analyze_llm_gateway_table_stats 加**跨实例共享的节流槽**。
--
-- 背景（runbook §10.107、§10.108）：
--   §10.53 给 analyze 加了 `pg_try_advisory_xact_lock`，2026-10-07 已上线。
--   上线后实测（runbook §10.107.6，2026-10-07 13:19~14:05）：**这把锁一次都没命中**——
--     · 判据 `partition_manager: analyze stats`（INFO 级、带 tables）精确计数
--     · 154 完成 15 趟 / 245 完成 16 趟（跨 245 换槽前后两代进程）
--     · **失败 0 次、跳过 0 次** ⇒ 31~32 趟全部执行，2 次/小时原封不动。
--
--   ⚠️ **「锁为何不生效」的成因至今未解释**，不要照抄旧结论：
--     · 旧结论写「两台 promote tick 偏移 6 分 01 秒 ≫ 持有 94 秒 ⇒ 永不争用」
--       —— **该数字是错的**。稳态逐小时实测偏移只有约 40 秒（-52s ~ +36s 来回摆）；
--       原文取的两趟都落在重启后的补跑上，不代表稳态一对。
--     · 已排除：两台不是不同库（DSN 同为 172.16.2.210:5432/llm_gateway）；
--       锁键不含实例身份（analyzeLockKey() 是常量 fnv64a("llm-gateway:analyze:")）。
--     · 未排除：「单趟其实 < 40s 所以两趟不重叠」与「重叠了但锁没拦住」。
--   ⇒ 但无论成因是哪一种，跨实例共享的节流槽都是必需的：它不依赖时序侥幸。
--
--   它解决的是 §10.53 同一段里并列写的另一件事（「互争 I/O」），
--   **没有**解决「两台各跑一遍全量」。按当前读数该项占数据库时间 81.0%。
--
-- 本迁移做两件事：
--   A. 建一张一行的共享状态表 + 两个函数：
--      · claim_llm_gateway_task_slot(task, min_interval) → boolean
--        原子占槽：距上次「开始或完成」不足 min_interval 则返回 false。
--      · complete_llm_gateway_task_slot(task)
--        跑完后记录完成时刻（供人工核查与回滚判据）。
--      ★ 原子性来自 `INSERT … ON CONFLICT DO UPDATE … WHERE … RETURNING`
--        单语句：**两个实例并发时只有一个能拿到返回行**，天然互斥，
--        **不依赖任何 advisory 锁**。这是与 §10.107 那把失效锁的本质区别。
--   B. 不改 analyze_llm_gateway_table_stats 本身，也不删 §10.53 那把锁——
--      两者正交：锁防「同一分钟内真重叠」，槽防「错开的两拍」。
--
-- ★ 为什么不用「对齐 ticker」：那只是把「各跑一遍」换成「每天必然撞锁」，
--   I/O 争抢窗口没消失，反而引入「谁抢到谁干活」的不确定性。
--
-- 阈值取 50 分钟：目标是 1 次/小时，不是「越多越好」。
--   收益按当前实测单趟约 50 秒估（§10.107.6 于 14:43 量到的 50s），
--   压到 1 次/小时约省 20 分钟数据库时间/天。
--   ⚠️ 不要再引用「93.79 秒 × 2 = 75 分钟/天，省 37 分钟」那个数：
--      93.79 秒是跨 26 天的历史均值，而 50 秒是 **838 被重启撤销之后**测的，
--      838 自身的收益至今未测（runbook §10.106.12）。
--
-- 回滚判据：.down.sql 只删表与函数，**不动调用方逻辑之外的东西**；
--   Go 侧对 undefined_table(42P01) 做降级 ⇒ 表不存在时行为退化为今天的样子。

BEGIN;

-- ── A1. 共享状态表 ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.llm_gateway_task_state (
    task_name         text        PRIMARY KEY,
    last_started_at   timestamptz NOT NULL,
    last_completed_at timestamptz,
    CONSTRAINT llm_gateway_task_state_task_name_nonempty
        CHECK (task_name <> '')
);

COMMENT ON TABLE public.llm_gateway_task_state IS
    '跨实例共享的周期任务节流槽：每个任务一行，claim_* 用原子占槽保证'
    '「N 分钟内只有一台实例跑本轮」。与 advisory 锁正交，锁防真重叠、槽防错开的两拍。';

-- ── A2. 原子占槽 ──────────────────────────────────────────────────────
-- ★ 本函数的正确性完全依赖 ON CONFLICT DO UPDATE 的 WHERE 与 RETURNING：
--   WHERE 不成立时 UPDATE 影响 0 行 ⇒ RETURNING 不返回行 ⇒ got 保持 NULL ⇒ 返回 false。
CREATE OR REPLACE FUNCTION public.claim_llm_gateway_task_slot(
    p_task_name    text,
    p_min_interval interval DEFAULT interval '50 minutes'
) RETURNS boolean
    LANGUAGE plpgsql
    AS $_$
DECLARE
    got boolean;
BEGIN
    IF p_task_name IS NULL OR btrim(p_task_name) = '' THEN
        RAISE EXCEPTION 'claim_llm_gateway_task_slot: p_task_name 不能为空';
    END IF;
    IF p_min_interval IS NULL OR p_min_interval < interval '0 second' THEN
        RAISE EXCEPTION 'claim_llm_gateway_task_slot: p_min_interval 非法: %', p_min_interval;
    END IF;

    INSERT INTO public.llm_gateway_task_state AS s (task_name, last_started_at)
    VALUES (p_task_name, now())
    ON CONFLICT (task_name) DO UPDATE
        SET last_started_at = now()
     WHERE COALESCE(s.last_completed_at, s.last_started_at) < now() - p_min_interval
    RETURNING TRUE INTO got;

    -- ★ got 为 NULL（不是 false）正是「WHERE 不成立 ⇒ 0 行 ⇒ RETURNING 空」的路径。
    RETURN COALESCE(got, false);
END;
$_$;

COMMENT ON FUNCTION public.claim_llm_gateway_task_slot(text, interval) IS
    '原子占槽。返回 true = 本实例拿到本轮；false = 距上次开始/完成不足 '
    'p_min_interval，本轮应跳过。并发下只有一个实例能拿到 true。';

-- ── A3. 记录完成时刻 ──────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION public.complete_llm_gateway_task_slot(p_task_name text)
RETURNS void
    LANGUAGE plpgsql
    AS $_$
BEGIN
    IF p_task_name IS NULL OR btrim(p_task_name) = '' THEN
        RAISE EXCEPTION 'complete_llm_gateway_task_slot: p_task_name 不能为空';
    END IF;

    UPDATE public.llm_gateway_task_state
       SET last_completed_at = now()
     WHERE task_name = p_task_name;
END;
$_$;

COMMENT ON FUNCTION public.complete_llm_gateway_task_slot(text) IS
    '跑完后记录完成时刻。只影响「槽位年龄」的判读与人工核查，不参与准入判定。';

COMMIT;