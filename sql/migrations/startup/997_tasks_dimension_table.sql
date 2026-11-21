-- ===========================================================================
-- File:          sql/migrations/startup/997_tasks_dimension_table.sql
-- Migration:     997
-- Database:      llm_gateway
-- Purpose:       创建任务维表，支持任务级别的元数据管理和会话聚合
--
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS + 单事务)
-- Dependencies:  547 (project_dim 表)
--
-- Background:
--   现有系统依赖 ACC 外部系统的任务信息（通过 X-Gw-Task-Id 传递），但本地
--   缺少任务实体的完整镜像。本迁移创建 tasks 表，用于：
--   1. 镜像 ACC 任务的基本信息
--   2. 存储从会话总结中提取的归一化标签
--   3. 支持任务级别的总结和统计
--   4. 为推断任务（非 ACC 来源）提供存储
--
-- Affected Rows: 0 (新表创建)
-- Breaking Change: NO (纯新增，不影响既有功能)
-- Rollback Script: 997_tasks_dimension_table.down.sql
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
-- ============================================================
-- 1. tasks 维表
-- ============================================================
CREATE TABLE IF NOT EXISTS public.tasks (
    task_id         VARCHAR(128) PRIMARY KEY,
    tenant_id       VARCHAR(255) NOT NULL,
    project_ref     TEXT,
    
    -- 任务元数据
    name            TEXT NOT NULL,
    description     TEXT,
    work_type       VARCHAR(50),
    
    -- 归一化标签 (从会话总结中提取并去重)
    canonical_tags  TEXT[] NOT NULL DEFAULT '{}',
    
    -- 任务级别总结 (由多个会话总结汇总而成)
    summary         TEXT,
    key_themes      TEXT[],
    
    -- 统计信息
    session_count   INTEGER NOT NULL DEFAULT 0,
    total_requests  INTEGER NOT NULL DEFAULT 0,
    total_cost_usd  DECIMAL(12,4) NOT NULL DEFAULT 0,
    
    -- 时间范围
    first_activity_at TIMESTAMPTZ,
    last_activity_at  TIMESTAMPTZ,
    
    -- 状态
    status          VARCHAR(20) NOT NULL DEFAULT 'active'
        CONSTRAINT tasks_status_check
        CHECK (status IN ('active', 'completed', 'archived')),
    
    -- 来源 (acc=从ACC同步, inferred=推断, manual=人工创建)
    source          VARCHAR(20) NOT NULL DEFAULT 'acc'
        CONSTRAINT tasks_source_check
        CHECK (source IN ('acc', 'inferred', 'manual')),
    
    -- 元数据 (存储扩展信息如 ACC 原始数据、推断依据等)
    metadata        JSONB,
    
    -- 审计字段
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 外键约束 (project_ref 可为空，允许无项目的独立任务)
    CONSTRAINT fk_tasks_project FOREIGN KEY (project_ref)
        REFERENCES public.project_dim(project_ref) ON DELETE SET NULL
);

-- 索引：按租户、状态、最近活动查询
CREATE INDEX IF NOT EXISTS idx_tasks_tenant_status 
    ON public.tasks (tenant_id, status, last_activity_at DESC);

-- 索引：按项目查询任务
CREATE INDEX IF NOT EXISTS idx_tasks_project 
    ON public.tasks (tenant_id, project_ref) 
    WHERE project_ref IS NOT NULL;

-- 索引：标签搜索 (GIN 索引支持数组包含查询)
CREATE INDEX IF NOT EXISTS idx_tasks_tags 
    ON public.tasks USING gin(canonical_tags) 
    WHERE canonical_tags != '{}';

-- 索引：按工作类型查询
CREATE INDEX IF NOT EXISTS idx_tasks_work_type
    ON public.tasks (tenant_id, work_type)
    WHERE work_type IS NOT NULL;

COMMENT ON TABLE public.tasks IS 
    '任务维表：本地镜像 ACC 任务 + 推断任务。用于任务级别的元数据管理和会话聚合。';

COMMENT ON COLUMN public.tasks.task_id IS 
    '任务唯一标识：ACC 任务使用 ACC ID，推断任务使用生成的 UUID';
COMMENT ON COLUMN public.tasks.canonical_tags IS 
    '归一化标签：从所属会话总结中提取并去重，支持语义搜索和分类';
COMMENT ON COLUMN public.tasks.summary IS 
    '任务级别总结：由所属会话的总结汇总生成';
COMMENT ON COLUMN public.tasks.key_themes IS 
    '关键主题：从所有会话中提取的高频主题';
COMMENT ON COLUMN public.tasks.source IS 
    'acc=从ACC同步 / inferred=系统推断 / manual=人工创建';

-- ============================================================
-- 2. 行级安全策略
-- ============================================================
ALTER TABLE public.tasks ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    -- 租户隔离策略
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'tasks'
          AND policyname = 'tasks_tenant_isolation'
    ) THEN
        CREATE POLICY tasks_tenant_isolation ON public.tasks
            USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
    END IF;

    -- 超级管理员绕过策略
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename = 'tasks'
          AND policyname = 'tasks_super_admin_bypass'
    ) THEN
        CREATE POLICY tasks_super_admin_bypass ON public.tasks
            USING (current_setting('app.current_role', true) = 'super_admin'
                OR current_setting('app.bypass_rls', true) = 'true');
    END IF;
END $$;

-- ============================================================
-- 3. 触发器：自动更新 updated_at
-- ============================================================
CREATE OR REPLACE FUNCTION public.update_tasks_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_tasks_updated_at ON public.tasks;
CREATE TRIGGER trigger_tasks_updated_at
    BEFORE UPDATE ON public.tasks
    FOR EACH ROW
    EXECUTE FUNCTION public.update_tasks_updated_at();

COMMIT;
