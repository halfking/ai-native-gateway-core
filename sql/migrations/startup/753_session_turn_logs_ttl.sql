-- ============================================================================
-- Migration 753: session_turn_logs configurable TTL
--
-- Purpose: 让 session_turn_logs 的保留时长由 settings_kv
--          lifecycle.session_turn_logs_ttl_hours 单一驱动，并提供
--          真正会被调用的清理入口。
--
-- 背景（docs/audit/2026-09-25-session-storage-audit-handoff.md §4 +
--       2026-09-27 对 Subtask 2 的批判式审计）：
--
--   初版（本迁移第一稿）的前提是错的，审计逐条证伪如下：
--
--   ① 「24h 硬编码清理」并不存在。
--      430 定义了 cleanup_expired_session_turn_logs()（谓词
--      `expires_at < NOW()`），但**全仓无任何调用方**：Go 无引用、无
--      pg_cron 注册、无 shell 调度。它只活在 baseline dump 与测试里。
--      也就是说 session_turn_logs 在生产上从未被清理过 —— 表无界增长。
--      本迁移不是「把既有清理参数化」，而是「第一次真正接上清理」。
--
--   ② 初版谓词 `expires_at < NOW() - make_interval(hours => p_ttl_hours)`
--      在语义上错了一整个 TTL。expires_at 由写入方烘焙为
--      「写入时刻 + TTL」，因此该谓词等价于「行龄 > 2×TTL」：
--      p_ttl_hours=24 时实际保留 48h，与「默认 24 = 保持原状」的承诺
--      相反。审计确认初版 commit message 里「与原硬编码行为逐字节一致」
--      是错的。
--
--   ③ 真正的 24h 硬编码在 Go 侧：domains/session/v2/turn_logs_writer.go
--      以 `time.Now().Add(24*time.Hour)` 显式写入 expires_at（生产写入
--      路径永远带该列，430 的列 DEFAULT 实际是死代码）。只改 SQL 谓词
--      无法让设置生效 —— 必须同时改写入方。
--
-- 本迁移的最终设计：
--   - expires_at 仍是保留期的唯一真相来源，由写入方按设置烘焙。
--   - 清理函数退化为「到期即删」：`expires_at < NOW()`，走 430 已建的
--     idx_session_turn_logs_expires 索引。
--   - p_ttl_hours 不再参与谓词，改为**安全联锁**：入参必须在 [1,168]，
--     越界直接 RAISE EXCEPTION（失败即停），避免一个坏配置静默变成
--     「全表删光」或「永不删」。
--
-- R72 审计修订（2026-09-27，在任何真库应用前原位定稿——252 库
-- schema_migrations 已核实无 752/753 记录）：
--   - 前版终稿的 DELETE 是无界单语句。本头注自证「session_turn_logs 在
--     生产上从未被清理过」，而部署 boot 段（archiveOldPartitionsIfNeeded）
--     即触发首扫：历史积压一次性 DELETE，长事务持行锁 + WAL 尖峰 +
--     表膨胀；且 bg 侧 5min 语句超时一旦触发整批回滚，下次重试要等 24h
--     tick——大积压库上清理永不收敛。
--   - 改为「每调用一有界批」：函数内 DELETE 带 LIMIT p_batch_size（主键
--     id 选批），返回**本次**删除行数；bg 调用方循环调用直到删空/超时。
--     每批是独立语句，超时最多损失当前批，已删进度不回滚。
--
-- 编号契约：原任务模板指 745，但该号已被占用，逐级上溯到首个空闲号：
--          745 = report_snapshots / 750 = usage_facts_daily_partition /
--          751 = usage_facts_partition_tz_pin（已 applied+verified 到 245 库）/
--          752 = mock_probe_history / 753 = 本迁移。
--          提交前须复核 origin/main 上 753 仍空闲 —— 主线持续推进。
--
-- 未采纳的初版设计（留档，说明为何否决）：
--   - CREATE INDEX idx_session_turn_logs_expires_at：与 430:275 的
--     idx_session_turn_logs_expires **同列重复**。在一张每 turn 写 7 行的
--     热表上挂第二个同列索引 = 纯粹的写放大 + 磁盘占用，换不来任何查询
--     收益。初稿注释以「与迁移族命名对齐」为由保留，属错误权衡，已移除。
-- ============================================================================

BEGIN;

CREATE OR REPLACE FUNCTION cleanup_session_turn_logs_by_ttl(
    p_ttl_hours  int,
    p_batch_size int DEFAULT 10000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    v_deleted bigint := 0;
BEGIN
    -- 安全联锁：p_ttl_hours 不参与 DELETE 谓词（保留期在写入时已烘焙进
    -- expires_at），这里只校验调用方与 settings_kv 的一致性。越界即抛错，
    -- 让 bg.PartitionManager 打日志告警，而不是静默做全表 DELETE。
    IF p_ttl_hours IS NULL OR p_ttl_hours < 1 OR p_ttl_hours > 168 THEN
        RAISE EXCEPTION
            'cleanup_session_turn_logs_by_ttl: p_ttl_hours=% must be within [1,168] (settings lifecycle.session_turn_logs_ttl_hours)',
            p_ttl_hours;
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 THEN
        RAISE EXCEPTION
            'cleanup_session_turn_logs_by_ttl: p_batch_size=% must be >= 1',
            p_batch_size;
    END IF;

    -- 到期即删，但**每调用只删一有界批**。expires_at 由
    -- domains/session/v2/turn_logs_writer.go 按
    -- lifecycle.session_turn_logs_ttl_hours 烘焙，故此谓词即「保留期已到」。
    -- 谓词命中 430:275 的 idx_session_turn_logs_expires，批内按主键 id 删除。
    -- 首扫积压由调用方循环消化：每批是独立语句，5min 语句超时最多损失
    -- 当前批，已删进度不回滚（无界单语句 DELETE 会整批回滚且下次重试要
    -- 等 24h tick——见头注 R72 修订）。
    DELETE FROM public.session_turn_logs
     WHERE id IN (
        SELECT id FROM public.session_turn_logs
         WHERE expires_at < NOW()
         LIMIT p_batch_size
     );

    GET DIAGNOSTICS v_deleted = ROW_COUNT;

    IF v_deleted > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session_turn_logs (ttl_hours=%, batch_size=%)',
            v_deleted, p_ttl_hours, p_batch_size;
    END IF;

    RETURN v_deleted;
END;
$$;

COMMENT ON FUNCTION cleanup_session_turn_logs_by_ttl(int, int) IS
    'Delete ONE bounded batch of expired session_turn_logs (expires_at < NOW(),
     LIMIT p_batch_size) and return the row count deleted by this call.
     Retention is NOT decided here: expires_at is baked at write time by
     domains/session/v2/turn_logs_writer.go from
     settings_kv.lifecycle.session_turn_logs_ttl_hours (default 24,
     clamped 1..168). p_ttl_hours is a fail-closed safety interlock only —
     it is validated against [1,168] and raises otherwise; it does not
     widen or narrow the DELETE. Backlog is drained by the caller looping
     until this returns 0 (bg.PartitionManager.cleanupSessionTurnLogsByTTL,
     which runs on the 24h archiveOldPartitionsIfNeeded tick, not the 1h
     runCleanup goroutine). Migration 753, 2026-09-27, R67 session-storage
     audit subtask 2 (handoff §4), semantics corrected by critical audit,
     batched by the R72 audit round.';

-- Ledger self-registration (710/734/738/740/742 惯例)。带存在性守卫：一次性
-- 测试库 (TEST_PG_URL 直灌裸 SQL) 没有 installer 基座的 schema_migrations
-- 表，守卫使迁移在两种环境都可执行。
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        INSERT INTO public.schema_migrations (version, description)
        VALUES (
            '753',
            'session_turn_logs configurable TTL (cleanup_session_turn_logs_by_ttl; retention baked at write time from lifecycle.session_turn_logs_ttl_hours), R67 session-storage audit subtask 2 (handoff §4)'
        )
        ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
    END IF;
END $$;

COMMIT;
