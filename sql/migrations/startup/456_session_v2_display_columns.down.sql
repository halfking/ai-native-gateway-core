-- Migration 456 (down): Rollback Session V2 display + summary columns
--
-- Purpose: 撤销 456 添加的展示/总结列。
--   警告：会删除数据！执行前请确保：
--     1. Feature Flag 已关闭 V2 路由
--     2. 已备份重要数据
--     3. 已验证系统完全回退到 V1
--
-- 所有权说明（重要）：
--   gateway.session_bodies.request_attachments / response_attachments
--   实际由 migration 430_sessions_v2_schema.sql 创建（DEFAULT '[]'::jsonb）；
--   migration 456 仅以 ADD COLUMN IF NOT EXISTS 幂等补齐，因此本 down
--   **不会** DROP 这两列（避免误删 430 创建的列及破坏下游依赖）。
--
-- Date: 2026-07-24
-- Author: session-v2 integration

BEGIN;

-- 索引
DROP INDEX IF EXISTS gateway.idx_sessions_summary_at;
DROP INDEX IF EXISTS gateway.idx_sessions_last_full_at;

-- session_turns 新增列
ALTER TABLE gateway.session_turns
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS tools,
    DROP COLUMN IF EXISTS attempt_no;

-- sessions 新增列
ALTER TABLE gateway.sessions
    DROP COLUMN IF EXISTS summary_quality,
    DROP COLUMN IF EXISTS summary_generated_at,
    DROP COLUMN IF EXISTS summary_model,
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS last_full_payload_at,
    DROP COLUMN IF EXISTS last_full_response,
    DROP COLUMN IF EXISTS last_full_request;

-- 注：session_bodies.request_attachments / response_attachments 不删除
-- （由 migration 430 创建，本迁移为幂等补齐）

-- 验证
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='gateway' AND table_name='sessions' AND column_name='last_full_payload_at'
    ) THEN
        RAISE EXCEPTION 'gateway.sessions.last_full_payload_at still exists after rollback';
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='gateway' AND table_name='session_turns' AND column_name='attempt_no'
    ) THEN
        RAISE EXCEPTION 'gateway.session_turns.attempt_no still exists after rollback';
    END IF;

    RAISE NOTICE '===== Migration 456 ROLLBACK SUCCESSFUL =====';
END;
$$;

COMMIT;
