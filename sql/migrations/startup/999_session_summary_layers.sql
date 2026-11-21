-- ===========================================================================
-- File:          sql/migrations/startup/999_session_summary_layers.sql
-- Migration:     999
-- Database:      llm_gateway
-- Purpose:       创建会话分层总结表，支持长会话的层级化摘要管理
--
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS + 单事务)
-- Dependencies:  无 (独立表)
--
-- Background:
--   长会话 (>50轮) 需要分层总结策略：
--   - Layer 1: 滑动窗口总结 (window) - 每10轮生成，实时更新
--   - Layer 2: 段落总结 (segment) - 每50轮或暂停24h生成
--   - Layer 3: 元总结 (meta) - 会话关闭时合并所有段落
--   本表存储各层级的总结，支持增量生成和历史追溯。
--
-- Affected Rows: 0 (新表创建)
-- Breaking Change: NO (纯新增，不影响既有功能)
-- Rollback Script: 999_session_summary_layers.down.sql
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- ============================================================
-- 1. session_summary_layers 表
-- ============================================================
CREATE TABLE IF NOT EXISTS public.session_summary_layers (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         VARCHAR(255) NOT NULL,
    gw_session_id     VARCHAR(128) NOT NULL,
    
    -- 层级信息
    layer_type        VARCHAR(20) NOT NULL
        CONSTRAINT session_summary_layers_type_check
        CHECK (layer_type IN ('window', 'segment', 'meta')),
    layer_sequence    INTEGER NOT NULL,  -- 同一层级内的序号 (0-based)
    
    -- 范围标识 (轮次范围)
    from_turn         INTEGER,
    to_turn           INTEGER,
    from_request_id   VARCHAR(128),
    to_request_id     VARCHAR(128),
    
    -- 总结内容
    summary           TEXT NOT NULL,
    key_points        TEXT[],  -- 关键要点列表
    
    -- 依赖关系 (meta 层依赖 segment 层)
    parent_layer_ids  BIGINT[],  -- 父层级 ID 列表
    
    -- 统计信息
    turn_count        INTEGER,   -- 覆盖的轮次数
    message_count     INTEGER,   -- 覆盖的消息数
    token_count       INTEGER,   -- 原始消息的 token 总数
    
    -- 审计字段
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    content_hash      VARCHAR(64) NOT NULL,  -- 用于幂等性检查
    
    -- 唯一约束：同一会话的同一层级同一序号只能有一条记录
    CONSTRAINT uq_session_layer 
        UNIQUE (tenant_id, gw_session_id, layer_type, layer_sequence)
);

-- ============================================================
-- 2. 索引
-- ============================================================

-- 按会话查询所有层级 (按序号排序)
CREATE INDEX IF NOT EXISTS idx_summary_layers_session 
    ON public.session_summary_layers (tenant_id, gw_session_id, layer_type, layer_sequence);

-- 按层级类型查询 (用于批量处理)
CREATE INDEX IF NOT EXISTS idx_summary_layers_type
    ON public.session_summary_layers (tenant_id, layer_type, created_at DESC);

-- 按创建时间查询 (用���清理旧窗口)
CREATE INDEX IF NOT EXISTS idx_summary_layers_created
    ON public.session_summary_layers (tenant_id, gw_session_id, created_at DESC)
    WHERE layer_type = 'window';

-- 按内容哈希查询 (用于去重)
CREATE INDEX IF NOT EXISTS idx_summary_layers_hash
    ON public.session_summary_layers (tenant_id, gw_session_id, content_hash);

-- ============================================================
-- 3. 注释
-- ============================================================
COMMENT ON TABLE public.session_summary_layers IS 
    '会话分层总结表：window(滑动窗口10轮) / segment(段落50轮) / meta(元总结)';

COMMENT ON COLUMN public.session_summary_layers.layer_type IS 
    'window=滑动窗口(10轮,最多保留5个) / segment=段落(50轮,永久保留) / meta=元总结(会话级)';

COMMENT ON COLUMN public.session_summary_layers.layer_sequence IS 
    '同一层级内的序号，从0开始。window/segment 按时间递增，meta 通常只有一条(序号0)';

COMMENT ON COLUMN public.session_summary_layers.parent_layer_ids IS 
    '父层级ID数组。meta 层的 parent_layer_ids 包含所有 segment 层的 id';

COMMENT ON COLUMN public.session_summary_layers.content_hash IS 
    'summary 内容的 SHA-256 哈希，用于幂等性检查和去重';

-- ============================================================
-- 4. 行级安全策略
-- ============================================================
ALTER TABLE public.session_summary_layers ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    -- 租户隔离策略
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'session_summary_layers'
          AND policyname = 'session_summary_layers_tenant_isolation'
    ) THEN
        CREATE POLICY session_summary_layers_tenant_isolation 
            ON public.session_summary_layers
            USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
    END IF;

    -- 超级管理员绕过策略
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'session_summary_layers'
          AND policyname = 'session_summary_layers_super_admin_bypass'
    ) THEN
        CREATE POLICY session_summary_layers_super_admin_bypass 
            ON public.session_summary_layers
            USING (current_setting('app.current_role', true) = 'super_admin'
                OR current_setting('app.bypass_rls', true) = 'true');
    END IF;
END $$;

COMMIT;
