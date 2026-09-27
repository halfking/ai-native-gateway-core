-- ============================================================================
-- Migration 753: session_turn_logs configurable TTL
--
-- Purpose: 把 session_turn_logs 表的 24h 硬编码 TTL 改为按入参可配（小时
--          单位），并提供 expires_at 索引兜底清理路径。
--
-- 背景（docs/audit/2026-09-25-session-storage-audit-handoff.md §4 + 2026-09-26
--          R67 session-storage audit subtask 2）：
--   - 现状：session_turn_logs.expires_at = NOW() + INTERVAL '24 hours'（430
--     schema 默认），且清理函数 cleanup_expired_session_turn_logs() 也是
--     硬编码 WHERE expires_at < NOW()，无法按部署场景调档（详情页查询窗口
--     与合规要求不同步）。
--   - 目标：让运行期可经 settings_kv.lifecycle.session_turn_logs_ttl_hours
--     控制清理阈值，默认 24h 不破既有行为；上限 168h (7 天) 与文档
--     docs/storage/2026-09-20-session-storage-decoupling-plan.md §5 S5 TTL
--     燃尽曲线对齐。
--
-- 编号契约：原任务模板指 745，但该号已被占用，逐级上溯到当前首个空闲号：
--          745 = report_snapshots（R63 收口设计预埋）
--          750 = usage_facts_daily_partition（R68 主线，与本任务同 24h 审计域）
--          751 = usage_facts_partition_tz_pin（已 applied+verified 到 245 库）
--          752 = mock_probe_history（R70 收口）
--          753 = 本迁移，走独立路径，不与 750/751/752 共享任何 schema_objects 形状。
--          提交前须复核 origin/main 上 753 仍空闲 —— 主线在 R70 期间仍在推进。
--
-- 设计契约：
--   ① cleanup_session_turn_logs_by_ttl(p_ttl_hours int) RETURNS bigint
--      删除 WHERE expires_at < NOW() - make_interval(hours => p_ttl_hours)；
--      入参 < 1 视作 1，NULL 视作 24，避免极端小值把表瞬时清空（spec 风险
--      约束"不要把 retention 改成 < 1h"）。
--   ② idx_session_turn_logs_expires_at 与 430 已建
--      idx_session_turn_logs_expires 共享 expires_at 列；命名差异仅出于
--      与本迁移族命名对齐（744/749/750 沿用 _at 后缀），不去重以保留
--      线上零侵入升级路径；运维可后续合并。
--   ③ schema_migrations 簿记：append-only 惯例（703 down 注记），down 移
--      除该行但不删函数 + 索引（函数被下游 SQL/Go 依赖，下游需重新创建
--      方可移除——本 down 仅恢复硬编码默认语义）。
-- ============================================================================

BEGIN;

CREATE OR REPLACE FUNCTION cleanup_session_turn_logs_by_ttl(p_ttl_hours int)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    v_ttl_hours int := GREATEST(COALESCE(p_ttl_hours, 24), 1);
    v_deleted bigint := 0;
BEGIN
    DELETE FROM public.session_turn_logs
     WHERE expires_at < NOW() - make_interval(hours => v_ttl_hours);

    GET DIAGNOSTICS v_deleted = ROW_COUNT;

    IF v_deleted > 0 THEN
        RAISE NOTICE 'Cleaned up % session_turn_logs older than % hours',
            v_deleted, v_ttl_hours;
    END IF;

    RETURN v_deleted;
END;
$$;

COMMENT ON FUNCTION cleanup_session_turn_logs_by_ttl(int) IS
    'Cleanup expired session_turn_logs older than p_ttl_hours (default
     behavior preserved at 24h, minimum enforced at 1h to keep retention
     sane). Called by bg.PartitionManager.runCleanup on the tick using
     settings_kv.lifecycle.session_turn_logs_ttl_hours. Migration 753
     (2026-09-26, R67 session-storage audit subtask 2, handoff §4).';

CREATE INDEX IF NOT EXISTS idx_session_turn_logs_expires_at
    ON public.session_turn_logs (expires_at);

-- Ledger self-registration (710/734/738/740/742 惯例)。带存在性守卫：一次
-- 性测试库 (TEST_PG_URL 直灌裸 SQL) 没有 installer 基座的
-- schema_migrations 表，守卫使迁移在两种环境都可执行。
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        INSERT INTO public.schema_migrations (version, description)
        VALUES (
            '753',
            'session_turn_logs configurable TTL (cleanup_session_turn_logs_by_ttl + idx_session_turn_logs_expires_at), R67 session-storage audit subtask 2 (handoff §4)'
        )
        ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
    END IF;
END $$;

COMMIT;
