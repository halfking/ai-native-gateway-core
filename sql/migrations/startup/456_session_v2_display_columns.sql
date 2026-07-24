-- Migration 456: Session V2 display + summary columns
--
-- Purpose: 增加会话快照的最后一轮全量快读、即时总结、attempt_no、附件 manifest 列
--   用于会话详情 UI 展示（最后一轮全量 + 即时总结）和回归 readiness。
--
-- 注意：
--   - 431 已被另一迁移占用（session_turns 增加 attachment_count 等），
--     这里使用未占用编号 456。
--   - gateway.session_bodies 上 request_attachments / response_attachments
--     已经由 migration 430 创建（DEFAULT '[]'::jsonb）。
--     本迁移以 IF NOT EXISTS 形式幂等地补一遍（不会创建重复列），
--     以保证会话 V2 详情工具的运行假设稳定。
--
-- Up-stream: 430_sessions_v2_schema.sql 必须先跑
-- Date: 2026-07-24
-- Author: session-v2 integration

BEGIN;

-- =============================================
-- gateway.sessions：最后一轮快读 + 总结
-- =============================================

ALTER TABLE gateway.sessions
    ADD COLUMN IF NOT EXISTS last_full_request JSONB,
    ADD COLUMN IF NOT EXISTS last_full_response JSONB,
    ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT,
    ADD COLUMN IF NOT EXISTS summary_model TEXT,
    ADD COLUMN IF NOT EXISTS summary_generated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS summary_quality TEXT
        CHECK (summary_quality IS NULL OR summary_quality IN ('rejected','partial','verified'));

COMMENT ON COLUMN gateway.sessions.last_full_request IS
    'Last turn full request payload (snapshot, used for session detail UI).';
COMMENT ON COLUMN gateway.sessions.last_full_response IS
    'Last turn full response payload (snapshot).';
COMMENT ON COLUMN gateway.sessions.last_full_payload_at IS
    'When last_full_request/response was captured.';
COMMENT ON COLUMN gateway.sessions.title IS
    'Auto-generated session title (one-line).';
COMMENT ON COLUMN gateway.sessions.summary IS
    'Inline summary of the conversation up to last_full_payload_at.';
COMMENT ON COLUMN gateway.sessions.summary_model IS
    'Model id used to produce summary.';
COMMENT ON COLUMN gateway.sessions.summary_generated_at IS
    'When summary was generated.';
COMMENT ON COLUMN gateway.sessions.summary_quality IS
    'Quality flag for the summary: rejected | partial | verified.';

-- =============================================
-- gateway.session_turns：attempt_no + 每轮一句话
-- =============================================

ALTER TABLE gateway.session_turns
    ADD COLUMN IF NOT EXISTS attempt_no INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT;

COMMENT ON COLUMN gateway.session_turns.attempt_no IS
    'Failover attempt index for this turn (0 = first attempt).';
COMMENT ON COLUMN gateway.session_turns.tools IS
    'Tools invoked during this turn (JSON array of tool_use records).';
COMMENT ON COLUMN gateway.session_turns.title IS
    'Turn-level one-line title (UI list rendering).';
COMMENT ON COLUMN gateway.session_turns.summary IS
    'Turn-level one-sentence summary.';

-- =============================================
-- gateway.session_bodies：附件 manifest（引用，不存 base64）
--   430 已经创建过同名同类型列；这里 IF NOT EXISTS 幂等补齐。
-- =============================================

ALTER TABLE gateway.session_bodies
    ADD COLUMN IF NOT EXISTS request_attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS response_attachments JSONB NOT NULL DEFAULT '[]'::jsonb;

-- =============================================
-- 验证（仅元数据检查，可放在事务内）
-- =============================================

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='gateway' AND table_name='sessions' AND column_name='last_full_payload_at'
    ) THEN
        RAISE EXCEPTION 'gateway.sessions.last_full_payload_at not created';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='gateway' AND table_name='session_turns' AND column_name='attempt_no'
    ) THEN
        RAISE EXCEPTION 'gateway.session_turns.attempt_no not created';
    END IF;

    RAISE NOTICE '===== Migration 456 SUCCESSFUL (columns) =====';
    RAISE NOTICE 'Sessions V2 display + summary columns added';
END;
$$;

COMMIT;

-- =============================================
-- 索引：必须放在事务外（CREATE INDEX CONCURRENTLY 不能在事务内）
--   - idx_sessions_last_full_at：last_full_payload_at 用于冷读检查
--   - idx_sessions_summary_at：summary_generated_at 用于 UI 提示"已总结"
--
-- 索引均为 partial（带 WHERE），建议在生产环境用 CONCURRENTLY 避免锁表；
-- 首次部署 / 测试库可正常 fall back（非 CONCURRENTLY）。
-- =============================================

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_last_full_at
    ON gateway.sessions (tenant_id, last_full_payload_at DESC)
    WHERE last_full_payload_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_summary_at
    ON gateway.sessions (tenant_id, summary_generated_at DESC)
    WHERE summary_generated_at IS NOT NULL;
