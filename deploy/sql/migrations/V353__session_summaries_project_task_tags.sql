-- V353: 扩展 session_summaries 表以支持项目、任务和标签维度
-- 2026-08-06: 添加会话管理功能所需的字段
--
-- 背景：
--   用户需求要求按项目、任务、标签等维度组织和过滤会话，
--   显示会话脉络和任务进度，汇总成本。
--
-- 变更：
--   1. gw_project_id: 项目ID（关联多个任务）
--   2. gw_task_id: 任务ID（与 request_logs.gw_task_id 对齐）
--   3. user_tags: 用户自定义标签数组（如 "feature", "bugfix", "探索"）
--   4. session_status: 会话状态（active, completed, abandoned）
--   5. search_vector: 全文搜索向量（标题+总结）
--
-- 索引：
--   - 按项目/任务过滤
--   - 按标签 GIN 索引
--   - 按状态过滤
--   - 全文搜索 GIN 索引

-- 1. 添加新列
ALTER TABLE session_summaries
  ADD COLUMN IF NOT EXISTS gw_project_id text,
  ADD COLUMN IF NOT EXISTS gw_task_id text,
  ADD COLUMN IF NOT EXISTS user_tags text[] DEFAULT '{}' NOT NULL,
  ADD COLUMN IF NOT EXISTS session_status varchar(20) DEFAULT 'active' NOT NULL;

-- 2. 添加检查约束
ALTER TABLE session_summaries
  ADD CONSTRAINT chk_session_status 
  CHECK (session_status IN ('active', 'completed', 'abandoned'));

-- 3. 添加全文搜索向量（生成列）
ALTER TABLE session_summaries
  ADD COLUMN IF NOT EXISTS search_vector tsvector 
  GENERATED ALWAYS AS (
    to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(summary, ''))
  ) STORED;

-- 4. 创建索引
CREATE INDEX IF NOT EXISTS idx_session_summaries_project 
  ON session_summaries(tenant_id, gw_project_id) 
  WHERE gw_project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_summaries_task 
  ON session_summaries(tenant_id, gw_task_id) 
  WHERE gw_task_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_summaries_user_tags 
  ON session_summaries USING gin(user_tags);

CREATE INDEX IF NOT EXISTS idx_session_summaries_status_time 
  ON session_summaries(tenant_id, session_status, last_request_at DESC);

CREATE INDEX IF NOT EXISTS idx_session_summaries_search 
  ON session_summaries USING gin(search_vector);

-- 5. 注释
COMMENT ON COLUMN session_summaries.gw_project_id IS '项目ID：一组相关任务的集合';
COMMENT ON COLUMN session_summaries.gw_task_id IS '任务ID：对应 request_logs.gw_task_id，一个具体的工作目标';
COMMENT ON COLUMN session_summaries.user_tags IS '用户自定义标签数组，用于分类和过滤会话';
COMMENT ON COLUMN session_summaries.session_status IS '会话状态：active(进行中), completed(已完成), abandoned(已放弃)';
COMMENT ON COLUMN session_summaries.search_vector IS '全文搜索向量：自动从标题和总结生成';
