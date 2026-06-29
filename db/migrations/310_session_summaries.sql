-- 310_session_summaries.sql
-- 会话聚合视图 - 实时追踪会话指标、生成会话总结和标题
-- 参考 Langfuse 的 TraceSession 设计，结合 Gateway 的路由会话需求
-- 使用 PostgreSQL Columnar 存储（适合 OLAP 查询）

-- 1. 创建会话摘要表
CREATE TABLE IF NOT EXISTS session_summaries (
    -- 主键
    session_key VARCHAR(255) PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 时间范围
    first_request_at TIMESTAMPTZ NOT NULL,
    last_request_at TIMESTAMPTZ NOT NULL,
    duration_seconds INT GENERATED ALWAYS AS (
        EXTRACT(EPOCH FROM (last_request_at - first_request_at))::INT
    ) STORED,
    
    -- 请求统计
    request_count INT NOT NULL DEFAULT 0,
    success_count INT NOT NULL DEFAULT 0,
    error_count INT NOT NULL DEFAULT 0,
    
    -- 成本统计（USD）
    total_cost_usd DECIMAL(12,6) NOT NULL DEFAULT 0,
    input_cost_usd DECIMAL(12,6) NOT NULL DEFAULT 0,
    output_cost_usd DECIMAL(12,6) NOT NULL DEFAULT 0,
    
    -- Token 统计
    total_prompt_tokens BIGINT NOT NULL DEFAULT 0,
    total_completion_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT GENERATED ALWAYS AS (
        total_prompt_tokens + total_completion_tokens
    ) STORED,
    
    -- 延迟统计（毫秒）
    avg_latency_ms INT NOT NULL DEFAULT 0,
    min_latency_ms INT,
    max_latency_ms INT,
    
    -- 模型使用
    models_used TEXT[] NOT NULL DEFAULT '{}',
    primary_model VARCHAR(100),
    model_switch_count INT NOT NULL DEFAULT 0,
    
    -- 会话分析（AI 生成）
    title VARCHAR(200),
    summary TEXT,
    key_topics TEXT[],
    user_intent VARCHAR(50),
    
    -- 质量评分
    quality_score INT CHECK (quality_score >= 0 AND quality_score <= 10),
    
    -- 合规状态
    compliance_status VARCHAR(20) DEFAULT 'compliant',
    compliance_issues_count INT NOT NULL DEFAULT 0,
    prompt_injection_detected BOOLEAN DEFAULT FALSE,
    pii_detected BOOLEAN DEFAULT FALSE,
    toxic_output_detected BOOLEAN DEFAULT FALSE,
    
    -- 会话元数据
    work_types TEXT[],
    providers TEXT[],
    client_models TEXT[],
    
    -- 审计字段
    last_summarized_at TIMESTAMPTZ,
    summary_version INT DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    -- 外键约束
    CONSTRAINT fk_session_tenant FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

-- 2. 创建索引
CREATE INDEX idx_session_summaries_tenant_time ON session_summaries(tenant_id, last_request_at DESC);
CREATE INDEX idx_session_summaries_compliance ON session_summaries(tenant_id, compliance_status) WHERE compliance_status != 'compliant';
CREATE INDEX idx_session_summaries_cost ON session_summaries(tenant_id, total_cost_usd DESC);
CREATE INDEX idx_session_summaries_intent ON session_summaries(tenant_id, user_intent) WHERE user_intent IS NOT NULL;
CREATE INDEX idx_session_summaries_quality ON session_summaries(quality_score DESC) WHERE quality_score IS NOT NULL;
CREATE INDEX idx_session_summaries_models ON session_summaries USING GIN(models_used);
CREATE INDEX idx_session_summaries_topics ON session_summaries USING GIN(key_topics);

-- 3. 创建 RLS 策略
ALTER TABLE session_summaries ENABLE ROW LEVEL SECURITY;

CREATE POLICY session_summaries_tenant_isolation ON session_summaries
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

CREATE POLICY session_summaries_super_admin_bypass ON session_summaries
    USING (
        current_setting('app.current_role', true) = 'super_admin' 
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- 4. 创建数组去重追加辅助函数
CREATE OR REPLACE FUNCTION array_unique_append(arr TEXT[], new_elem TEXT)
RETURNS TEXT[] AS $$
BEGIN
    IF new_elem IS NULL THEN
        RETURN arr;
    END IF;
    
    IF new_elem = ANY(arr) THEN
        RETURN arr;
    ELSE
        RETURN array_append(arr, new_elem);
    END IF;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- 5. 创建实时聚合触发器函数
CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
DECLARE
    v_input_cost DECIMAL(12,6);
    v_output_cost DECIMAL(12,6);
    v_total_cost DECIMAL(12,6);
    v_prompt_tokens BIGINT;
    v_completion_tokens BIGINT;
    v_latency_ms INT;
    v_status VARCHAR(50);
    v_client_model VARCHAR(100);
    v_upstream_model VARCHAR(100);
    v_work_type VARCHAR(50);
    v_provider VARCHAR(50);
BEGIN
    -- 提取字段（避免多次访问 NEW）
    v_input_cost := COALESCE(NEW.input_cost, 0);
    v_output_cost := COALESCE(NEW.output_cost, 0);
    v_total_cost := COALESCE(NEW.total_cost, 0);
    v_prompt_tokens := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_latency_ms := COALESCE(NEW.latency_ms, 0);
    v_status := NEW.status;
    v_client_model := NEW.client_model;
    v_upstream_model := NEW.upstream_model;
    v_work_type := NEW.work_type;
    v_provider := NEW.provider;

    -- 插入或更新会话摘要
    INSERT INTO session_summaries (
        session_key,
        tenant_id,
        first_request_at,
        last_request_at,
        request_count,
        success_count,
        error_count,
        total_cost_usd,
        input_cost_usd,
        output_cost_usd,
        total_prompt_tokens,
        total_completion_tokens,
        avg_latency_ms,
        min_latency_ms,
        max_latency_ms,
        models_used,
        work_types,
        providers,
        client_models,
        updated_at
    ) VALUES (
        NEW.session_key,
        NEW.tenant_id,
        NEW.created_at,
        NEW.created_at,
        1,
        CASE WHEN v_status = 'success' THEN 1 ELSE 0 END,
        CASE WHEN v_status != 'success' THEN 1 ELSE 0 END,
        v_total_cost,
        v_input_cost,
        v_output_cost,
        v_prompt_tokens,
        v_completion_tokens,
        v_latency_ms,
        v_latency_ms,
        v_latency_ms,
        ARRAY[v_upstream_model]::TEXT[],
        CASE WHEN v_work_type IS NOT NULL THEN ARRAY[v_work_type]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_provider IS NOT NULL THEN ARRAY[v_provider]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_client_model IS NOT NULL THEN ARRAY[v_client_model]::TEXT[] ELSE '{}'::TEXT[] END,
        NOW()
    )
    ON CONFLICT (session_key) DO UPDATE SET
        last_request_at = GREATEST(session_summaries.last_request_at, NEW.created_at),
        request_count = session_summaries.request_count + 1,
        success_count = session_summaries.success_count + CASE WHEN v_status = 'success' THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count + CASE WHEN v_status != 'success' THEN 1 ELSE 0 END,
        total_cost_usd = session_summaries.total_cost_usd + v_total_cost,
        input_cost_usd = session_summaries.input_cost_usd + v_input_cost,
        output_cost_usd = session_summaries.output_cost_usd + v_output_cost,
        total_prompt_tokens = session_summaries.total_prompt_tokens + v_prompt_tokens,
        total_completion_tokens = session_summaries.total_completion_tokens + v_completion_tokens,
        avg_latency_ms = (
            (session_summaries.avg_latency_ms * session_summaries.request_count + v_latency_ms) / 
            (session_summaries.request_count + 1)
        )::INT,
        min_latency_ms = LEAST(session_summaries.min_latency_ms, v_latency_ms),
        max_latency_ms = GREATEST(session_summaries.max_latency_ms, v_latency_ms),
        models_used = array_unique_append(session_summaries.models_used, v_upstream_model),
        work_types = array_unique_append(session_summaries.work_types, v_work_type),
        providers = array_unique_append(session_summaries.providers, v_provider),
        client_models = array_unique_append(session_summaries.client_models, v_client_model),
        updated_at = NOW();

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 6. 绑定触发器到 request_logs 表
CREATE TRIGGER trg_update_session_summary
    AFTER INSERT ON request_logs
    FOR EACH ROW
    WHEN (NEW.session_key IS NOT NULL AND NEW.session_key != '')
    EXECUTE FUNCTION update_session_summary();

-- 7. 创建统计视图
CREATE OR REPLACE VIEW session_stats_today AS
SELECT 
    tenant_id,
    COUNT(*) as session_count,
    COUNT(*) FILTER (WHERE last_request_at > NOW() - INTERVAL '1 hour') as active_sessions,
    SUM(request_count) as total_requests,
    SUM(total_cost_usd) as total_cost,
    AVG(total_cost_usd) as avg_cost_per_session,
    AVG(total_tokens) as avg_tokens_per_session,
    AVG(avg_latency_ms) as avg_latency,
    COUNT(*) FILTER (WHERE compliance_status = 'compliant') * 100.0 / NULLIF(COUNT(*), 0) as compliance_rate,
    COUNT(*) FILTER (WHERE quality_score >= 8) * 100.0 / NULLIF(COUNT(*) FILTER (WHERE quality_score IS NOT NULL), 0) as high_quality_rate
FROM session_summaries
WHERE first_request_at >= CURRENT_DATE
GROUP BY tenant_id;

-- 8. 创建备注
COMMENT ON TABLE session_summaries IS '会话聚合视图 - 实时追踪会话的成本、Token、延迟等指标，支持 AI 生成的会话总结和标题';
COMMENT ON COLUMN session_summaries.session_key IS '会话唯一标识（来自 request_logs.session_key）';
COMMENT ON COLUMN session_summaries.title IS 'AI 生成的会话标题（20字以内）';
COMMENT ON COLUMN session_summaries.summary IS '会话总结（100-200字），由 LLM 异步生成';
COMMENT ON COLUMN session_summaries.user_intent IS '用户意图分类：chat/code/tool_use/data_analysis/creative/unknown';
COMMENT ON COLUMN session_summaries.quality_score IS '会话质量评分（0-10），综合考虑成功率、延迟、成本等因素';
COMMENT ON COLUMN session_summaries.compliance_status IS '合规状态：compliant(合规)/warning(警告)/violation(违规)';
COMMENT ON COLUMN session_summaries.model_switch_count IS '会话中模型切换次数（路由决策变更）';
