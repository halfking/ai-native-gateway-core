-- ===========================================================================
-- File:          sql/migrations/startup/998_session_summaries_task_association.sql
-- Migration:     998
-- Database:      llm_gateway
-- Purpose:       扩展 session_summaries 表，支持任务关联和会话去重
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS + 单事务)
-- Dependencies:  997 (tasks 表), 606 (agent_type/expert_type/tags 基线)
--
-- Background:
--   现有 session_summaries 表存储单个会话的总结，但缺少：
--   1. 与任务的关联关系
--   2. 相似会话的识别和去重
--   3. 规范会话的指向
--   本迁移添加这些字段，支持任务级别的会话聚合和去重。
--
-- Affected Rows: 0 (ADD COLUMN 带 DEFAULT，PG11+ 快速路径)
-- Breaking Change: NO (新列带 DEFAULT，既有代码不引用即兼容)
-- Rollback Script: 998_session_summaries_task_association.down.sql
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
-- ============================================================
-- 1. 扩展 session_summaries 表
-- ============================================================

-- 任务关联
ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS task_id VARCHAR(128);

-- 相似会话列表 (存储语义相似的其他会话 key)
ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS similar_session_keys TEXT[] DEFAULT '{}';

-- 去重标记
ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS is_duplicate BOOLEAN NOT NULL DEFAULT FALSE;

-- 规范会话指针 (若为重复会话，指向规范会话的 session_key)
ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS canonical_session_key VARCHAR(128);

-- 分层总结标记 (标识该会话是否已生成分层总结)
ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS has_layered_summary BOOLEAN NOT NULL DEFAULT FALSE;

-- ============================================================
-- 2. 索引
-- ============================================================

-- 按任务查询会话
CREATE INDEX IF NOT EXISTS idx_session_summaries_task 
    ON public.session_summaries (tenant_id, task_id, created_at DESC)
    WHERE task_id IS NOT NULL;

-- 按规范会话查询重复会话
CREATE INDEX IF NOT EXISTS idx_session_summaries_canonical 
    ON public.session_summaries (tenant_id, canonical_session_key)
    WHERE canonical_session_key IS NOT NULL;

-- 查询需要去重的会话 (未标记且有相似会话)
CREATE INDEX IF NOT EXISTS idx_session_summaries_dedup_candidates
    ON public.session_summaries (tenant_id, is_duplicate, created_at DESC)
    WHERE is_duplicate = FALSE AND similar_session_keys != '{}';

-- 查询需要生成分层总结的会话
CREATE INDEX IF NOT EXISTS idx_session_summaries_layered_pending
    ON public.session_summaries (tenant_id, has_layered_summary, created_at DESC)
    WHERE has_layered_summary = FALSE;

-- ============================================================
-- 3. 外键约束 (可选，允许 task_id 不存在于 tasks 表)
-- ============================================================
-- 注意: 不添加外键约束，因为 task_id 可能来自未同步的 ACC 任务
-- 如需强制引用完整性，可取消注释以下代码:
/*
ALTER TABLE public.session_summaries
    ADD CONSTRAINT fk_session_summaries_task 
    FOREIGN KEY (task_id) REFERENCES public.tasks(task_id) 
    ON DELETE SET NULL;
*/

-- ============================================================
-- 4. 注释
-- ============================================================
COMMENT ON COLUMN public.session_summaries.task_id IS 
    '关联任务ID (来自ACC或推断)，用于任务级别的会话聚合';

COMMENT ON COLUMN public.session_summaries.similar_session_keys IS 
    '语义相似的其他会话 (余弦相似度 > 0.85)，用于去重和关联展示';

COMMENT ON COLUMN public.session_summaries.is_duplicate IS 
    '是否被识别为重复会话 (相似度高且内容重叠)';

COMMENT ON COLUMN public.session_summaries.canonical_session_key IS 
    '若为重复会话，指向规范会话 (通常是最早创建或最完整的那个)';

COMMENT ON COLUMN public.session_summaries.has_layered_summary IS 
    '是否已生成分层总结 (仅长会话需要)';

-- 台账自登记（≥952 双账本纪律：迁移必须自己把行写进 public.schema_migrations
-- —— 995/996 同款；缺此段 ⇒ 任何通道应用后运行时台账无行 ⇒ 连续性门/漂移报告假红。
-- 2026-11-22 第 62 增量审计轮补：0f545826a 落地时三迁移均漏此面）
INSERT INTO public.schema_migrations (version, description)
VALUES ('998', 'session_summaries task-association columns: task_id, similar_session_keys, is_duplicate, canonical_session_key, has_layered_summary')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
