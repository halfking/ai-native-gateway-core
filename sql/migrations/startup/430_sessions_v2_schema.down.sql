-- Migration 430 (down): Rollback Sessions V2 Schema
-- 
-- Purpose: 完全删除会话V2架构，恢复到只使用request_logs的状态
-- 
-- 警告：此操作会删除所有V2表中的数据！
--        执行前请确保：
--        1. Feature Flag已关闭V2路由
--        2. 已备份重要数据
--        3. 已验证系统完全回退到V1
-- 
-- Author: llm-gateway-ops
-- Date: 2026-07-17

BEGIN;

-- =============================================
-- 1. 删除清理函数
-- =============================================

DROP FUNCTION IF EXISTS cleanup_expired_session_turn_logs();
DROP FUNCTION IF EXISTS ensure_sessions_v2_partitions(DATE);

-- =============================================
-- 2. 删除表（CASCADE会自动删除所有分区和依赖）
-- =============================================

DROP TABLE IF EXISTS public.session_turn_logs CASCADE;
DROP TABLE IF EXISTS public.session_bodies CASCADE;
DROP TABLE IF EXISTS public.session_turns CASCADE;
DROP TABLE IF EXISTS public.sessions CASCADE;

-- =============================================
-- 3. 验证回滚
-- =============================================

DO $$
BEGIN
    -- 验证表已删除
    IF EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'sessions') THEN
        RAISE EXCEPTION 'Table public.sessions still exists after rollback';
    END IF;
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_turns') THEN
        RAISE EXCEPTION 'Table public.session_turns still exists after rollback';
    END IF;
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_bodies') THEN
        RAISE EXCEPTION 'Table public.session_bodies still exists after rollback';
    END IF;
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'gateway' AND tablename = 'session_turn_logs') THEN
        RAISE EXCEPTION 'Table public.session_turn_logs still exists after rollback';
    END IF;
    
    RAISE NOTICE '===== Migration 430 ROLLBACK SUCCESSFUL =====';
    RAISE NOTICE 'All sessions V2 tables and functions have been removed';
    RAISE NOTICE 'System reverted to request_logs only';
END;
$$;

COMMIT;
