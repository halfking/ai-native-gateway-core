-- 351_session_analytics_tables.sql
-- 会话全景分析插件数据模型（5 张表）
--   session_tags                多维标签
--   session_request_summaries   逐步请求/回复摘要
--   session_embeddings          聚类用向量（pgvector，可选）
--   session_clusters            相似会话聚类
--   session_cluster_members     聚类成员
--   session_optimization_suggestions  优化建议
--
-- 所有表通过 gw_session_id 关联 session_summaries.session_key。
-- 启用 RLS 租户隔离（与 session_summaries 一致）。

BEGIN;

-- ============================================================
-- 1. session_tags — 多维标签
-- ============================================================
CREATE TABLE IF NOT EXISTS session_tags (
    id BIGSERIAL PRIMARY KEY,
    gw_session_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    tag_key VARCHAR(50) NOT NULL,   -- task|client|llm|topic|intent|quality|custom
    tag_value TEXT NOT NULL,
    tag_source VARCHAR(20) NOT NULL DEFAULT 'auto',  -- auto|llm|manual
    confidence REAL DEFAULT 1.0,
    created_by VARCHAR(100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_session_tag UNIQUE (gw_session_id, tag_key, tag_value)
);

CREATE INDEX IF NOT EXISTS idx_session_tags_session ON session_tags(gw_session_id);
CREATE INDEX IF NOT EXISTS idx_session_tags_tenant_key ON session_tags(tenant_id, tag_key);
CREATE INDEX IF NOT EXISTS idx_session_tags_value ON session_tags USING GIN (to_tsvector('simple', tag_value));

ALTER TABLE session_tags ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_tags_tenant_isolation ON session_tags
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_tags_super_admin_bypass ON session_tags
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE session_tags IS '会话多维标签：task/client/llm/topic/intent/quality/custom';

-- ============================================================
-- 2. session_request_summaries — 逐步请求/回复摘要
-- ============================================================
CREATE TABLE IF NOT EXISTS session_request_summaries (
    id BIGSERIAL PRIMARY KEY,
    gw_session_id VARCHAR(128) NOT NULL,
    request_id TEXT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    step_index INT NOT NULL,
    request_summary TEXT,
    response_summary TEXT,
    is_llm_generated BOOLEAN NOT NULL DEFAULT FALSE,
    prompt_tokens INT DEFAULT 0,
    completion_tokens INT DEFAULT 0,
    cost_usd NUMERIC(14,8) DEFAULT 0,
    latency_ms INT DEFAULT 0,
    tool_calls_summary TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_request_summary UNIQUE (gw_session_id, request_id)
);

CREATE INDEX IF NOT EXISTS idx_request_summaries_session
    ON session_request_summaries(gw_session_id, step_index);
CREATE INDEX IF NOT EXISTS idx_request_summaries_tenant
    ON session_request_summaries(tenant_id, created_at DESC);

ALTER TABLE session_request_summaries ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_request_summaries_tenant_isolation ON session_request_summaries
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_request_summaries_super_admin_bypass ON session_request_summaries
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE session_request_summaries IS '逐步请求/回复摘要（规则默认+LLM可选）';

-- ============================================================
-- 3. session_embeddings — 聚类用向量
--    （需 pgvector 扩展；若未安装则建表降级为仅存 hash，聚类退化为纯规则）
-- ============================================================
DO $$
BEGIN
    BEGIN
        CREATE EXTENSION IF NOT EXISTS vector;
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'pgvector extension unavailable; session_embeddings will store hash-only';
    END;
END $$;

CREATE TABLE IF NOT EXISTS session_embeddings (
    gw_session_id VARCHAR(128) PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    embedding vector(1536),
    content_hash VARCHAR(64),
    model VARCHAR(100),
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_embeddings_tenant
    ON session_embeddings(tenant_id, generated_at DESC);

ALTER TABLE session_embeddings ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_embeddings_tenant_isolation ON session_embeddings
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_embeddings_super_admin_bypass ON session_embeddings
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE session_embeddings IS '会话摘要向量化（聚类用，依赖 pgvector，可选）';

-- ============================================================
-- 4. session_clusters + session_cluster_members — 相似会话聚类
-- ============================================================
CREATE TABLE IF NOT EXISTS session_clusters (
    cluster_id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    coarse_key VARCHAR(255),       -- 粗聚类键（intent|work_type|model|topic0）
    label VARCHAR(200),            -- LLM 生成的语义标签
    topic_path TEXT[] DEFAULT '{}',
    centroid_summary TEXT,         -- 聚类中心摘要
    member_count INT NOT NULL DEFAULT 0,
    avg_cost_usd NUMERIC(12,6) DEFAULT 0,
    avg_quality_score REAL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_clusters_tenant
    ON session_clusters(tenant_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_clusters_coarse
    ON session_clusters(tenant_id, coarse_key);

ALTER TABLE session_clusters ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_clusters_tenant_isolation ON session_clusters
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_clusters_super_admin_bypass ON session_clusters
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

CREATE TABLE IF NOT EXISTS session_cluster_members (
    cluster_id VARCHAR(64) NOT NULL,
    gw_session_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    score REAL DEFAULT 1.0,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cluster_id, gw_session_id)
);

CREATE INDEX IF NOT EXISTS idx_cluster_members_session
    ON session_cluster_members(gw_session_id);
CREATE INDEX IF NOT EXISTS idx_cluster_members_tenant
    ON session_cluster_members(tenant_id, cluster_id);

ALTER TABLE session_cluster_members ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_cluster_members_tenant_isolation ON session_cluster_members
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_cluster_members_super_admin_bypass ON session_cluster_members
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE session_clusters IS '相似会话聚类（混合：标签粗分+向量细分）';
COMMENT ON TABLE session_cluster_members IS '聚类成员映射';

-- ============================================================
-- 5. session_optimization_suggestions — 优化建议
-- ============================================================
CREATE TABLE IF NOT EXISTS session_optimization_suggestions (
    id BIGSERIAL PRIMARY KEY,
    gw_session_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    category VARCHAR(50) NOT NULL,   -- prompt|tool|model|policy|session
    severity VARCHAR(20) NOT NULL DEFAULT 'info',  -- info|warn|action_required
    title VARCHAR(200) NOT NULL,
    description TEXT,
    potential_savings_tokens BIGINT DEFAULT 0,
    potential_savings_cost NUMERIC(12,6) DEFAULT 0,
    evidence JSONB,                  -- 支撑数据（比值/明细）
    applied BOOLEAN NOT NULL DEFAULT FALSE,
    applied_at TIMESTAMPTZ,
    applied_by VARCHAR(100),
    dismissed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_opt_session
    ON session_optimization_suggestions(gw_session_id);
CREATE INDEX IF NOT EXISTS idx_session_opt_tenant
    ON session_optimization_suggestions(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_opt_unapplied
    ON session_optimization_suggestions(tenant_id, severity) WHERE NOT applied AND NOT dismissed;

ALTER TABLE session_optimization_suggestions ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_opt_suggestions_tenant_isolation ON session_optimization_suggestions
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_opt_suggestions_super_admin_bypass ON session_optimization_suggestions
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE session_optimization_suggestions IS '优化建议与潜在节省量化（规则检测引擎产出）';

COMMIT;
