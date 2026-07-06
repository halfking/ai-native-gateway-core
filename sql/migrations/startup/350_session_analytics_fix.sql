-- 350_session_analytics_fix.sql
-- 修复 310_session_summaries 的致命断点：
--   1. trigger update_session_summary() 引用了 request_logs 上不存在的列
--      (session_key, created_at, input_cost, output_cost, total_cost,
--       upstream_model, status, provider)。导致整条聚合链路从未生效。
--   2. session_summaries.session_key 与 request_logs.gw_session_id 语义不一致。
--
-- 本迁移采用「映射表 + 视图」解耦策略（用户决策：用视图/映射表解耦）：
--   - 新增 session_dim 映射表：gw_session_id ↔ session_key ↔ task_id + 生命周期
--   - 重写 trigger 使用真实列名（gw_session_id/ts/cost_usd/outbound_model/...）
--   - 创建 v_session_analytics 视图统一对外暴露 gw_session_id
--   - 不改 session_summaries 列名（session_key 列值 = gw_session_id，加注释说明）
--
-- 列映射（trigger 旧引用 → request_logs 实际列）：
--   session_key  → gw_session_id
--   created_at   → ts
--   total_cost   → cost_usd
--   upstream_model → outbound_model
--   status       → success (bool → 'success'/'error' 文本)
--   provider     → providers.code (LEFT JOIN by provider_id)
--   input_cost   → 计算：cost_usd × prompt_token_ratio
--   output_cost  → 计算：cost_usd × completion_token_ratio
--
-- 分区表说明：request_logs 是 RANGE 分区表，FOR EACH ROW 触发器定义在父表上
-- 会自动作用于已 ATTACH 的子分区。

BEGIN;

-- ============================================================
-- 1. session_dim 映射表
-- ============================================================
CREATE TABLE IF NOT EXISTS session_dim (
    gw_session_id VARCHAR(128) PRIMARY KEY,
    session_key   VARCHAR(255) NOT NULL,  -- = gw_session_id（兼容 session_summaries）
    tenant_id     VARCHAR(255) NOT NULL,
    task_id       VARCHAR(128),
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',  -- active|closed|idle
    first_request_at TIMESTAMPTZ,
    last_active_at  TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_session_dim_tenant
    ON session_dim(tenant_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_dim_task
    ON session_dim(tenant_id, task_id) WHERE task_id IS NOT NULL;

ALTER TABLE session_dim ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_dim_tenant_isolation ON session_dim
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_dim_super_admin_bypass ON session_dim
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- ============================================================
-- 2. 删除旧的、坏掉的 trigger（从不生效，安全删除）
-- ============================================================
DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs;
DROP FUNCTION IF EXISTS update_session_summary();

-- ============================================================
-- 3. 重写 trigger 函数（使用真实列名 + UPSERT session_dim）
-- ============================================================
CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
DECLARE
    v_prompt_tokens   BIGINT;
    v_completion_tokens BIGINT;
    v_total_tokens    BIGINT;
    v_cost            DECIMAL(14,8);
    v_input_cost      DECIMAL(12,6);
    v_output_cost     DECIMAL(12,6);
    v_latency_ms      INT;
    v_is_success      BOOLEAN;
    v_client_model    VARCHAR(100);
    v_upstream_model  VARCHAR(100);
    v_work_type       VARCHAR(50);
    v_provider_code   VARCHAR(100);
    v_gw_session_id   VARCHAR(128);
    v_token_ratio     DECIMAL(10,6);
BEGIN
    -- 只处理带 gw_session_id 的会话请求
    v_gw_session_id := NEW.gw_session_id;
    IF v_gw_session_id IS NULL OR v_gw_session_id = '' THEN
        RETURN NEW;
    END IF;

    -- 提取字段（避免多次访问 NEW）
    v_prompt_tokens     := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_total_tokens      := v_prompt_tokens + v_completion_tokens;
    v_cost              := COALESCE(NEW.cost_usd, 0);
    v_latency_ms        := COALESCE(NEW.latency_ms, 0);
    v_is_success        := NEW.success;
    v_client_model      := NEW.client_model;
    v_upstream_model    := NEW.outbound_model;
    v_work_type         := NEW.work_type;

    -- 按比例拆分 input/output cost（request_logs 无独立列）
    IF v_total_tokens > 0 THEN
        v_token_ratio := v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6);
    ELSE
        v_token_ratio := 0.5;
    END IF;
    v_input_cost  := (v_cost * v_token_ratio)::DECIMAL(12,6);
    v_output_cost := (v_cost - v_input_cost)::DECIMAL(12,6);

    -- 解析 provider code（LEFT JOIN，失败则 NULL）
    SELECT p.code INTO v_provider_code
    FROM providers p
    WHERE p.id = NEW.provider_id
    LIMIT 1;

    -- ---- UPSERT session_dim（维护会话生命周期） ----
    INSERT INTO session_dim (
        gw_session_id, session_key, tenant_id, task_id, status,
        first_request_at, last_active_at, created_at
    ) VALUES (
        v_gw_session_id, v_gw_session_id, NEW.tenant_id, NEW.gw_task_id, 'active',
        NEW.ts, NEW.ts, NOW()
    )
    ON CONFLICT (gw_session_id) DO UPDATE SET
        last_active_at = GREATEST(session_dim.last_active_at, NEW.ts),
        status = CASE WHEN session_dim.status = 'closed' THEN 'active' ELSE session_dim.status END,
        task_id = COALESCE(session_dim.task_id, NEW.gw_task_id);

    -- ---- UPSERT session_summaries（实时聚合） ----
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
        v_gw_session_id,
        NEW.tenant_id,
        NEW.ts,
        NEW.ts,
        1,
        CASE WHEN v_is_success THEN 1 ELSE 0 END,
        CASE WHEN v_is_success THEN 0 ELSE 1 END,
        v_cost,
        v_input_cost,
        v_output_cost,
        v_prompt_tokens,
        v_completion_tokens,
        v_latency_ms,
        v_latency_ms,
        v_latency_ms,
        CASE WHEN v_upstream_model IS NOT NULL THEN ARRAY[v_upstream_model]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_work_type IS NOT NULL THEN ARRAY[v_work_type]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_provider_code IS NOT NULL THEN ARRAY[v_provider_code]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_client_model IS NOT NULL THEN ARRAY[v_client_model]::TEXT[] ELSE '{}'::TEXT[] END,
        NOW()
    )
    ON CONFLICT (session_key) DO UPDATE SET
        last_request_at = GREATEST(session_summaries.last_request_at, NEW.ts),
        request_count = session_summaries.request_count + 1,
        success_count = session_summaries.success_count + CASE WHEN v_is_success THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count + CASE WHEN v_is_success THEN 0 ELSE 1 END,
        total_cost_usd = session_summaries.total_cost_usd + v_cost,
        input_cost_usd = session_summaries.input_cost_usd + v_input_cost,
        output_cost_usd = session_summaries.output_cost_usd + v_output_cost,
        total_prompt_tokens = session_summaries.total_prompt_tokens + v_prompt_tokens,
        total_completion_tokens = session_summaries.total_completion_tokens + v_completion_tokens,
        avg_latency_ms = (
            (session_summaries.avg_latency_ms * session_summaries.request_count + v_latency_ms)
            / (session_summaries.request_count + 1)
        )::INT,
        min_latency_ms = LEAST(session_summaries.min_latency_ms, v_latency_ms),
        max_latency_ms = GREATEST(session_summaries.max_latency_ms, v_latency_ms),
        models_used = array_unique_append(session_summaries.models_used, v_upstream_model),
        work_types = array_unique_append(session_summaries.work_types, v_work_type),
        providers = array_unique_append(session_summaries.providers, v_provider_code),
        client_models = array_unique_append(session_summaries.client_models, v_client_model),
        primary_model = (
            SELECT m FROM unnest(session_summaries.models_used) AS m
            ORDER BY m LIMIT 1
        ),
        updated_at = NOW();

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- 4. 重新绑定 trigger 到 request_logs 父表
--    （分区表：父表 trigger 自动作用于 ATTACH 的子分区）
-- ============================================================
CREATE TRIGGER trg_update_session_summary
    AFTER INSERT ON request_logs
    FOR EACH ROW
    WHEN (NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id != '')
    EXECUTE FUNCTION update_session_summary();

-- ============================================================
-- 5. 修复 session_stats_today 视图（字段名 + 时间列）
-- ============================================================
CREATE OR REPLACE VIEW session_stats_today AS
SELECT
    tenant_id,
    COUNT(*) AS session_count,
    COUNT(*) FILTER (WHERE last_request_at > NOW() - INTERVAL '1 hour') AS active_sessions,
    SUM(request_count) AS total_requests,
    SUM(total_cost_usd) AS total_cost,
    AVG(total_cost_usd) AS avg_cost_per_session,
    AVG(total_tokens) AS avg_tokens_per_session,
    AVG(avg_latency_ms) AS avg_latency,
    COUNT(*) FILTER (WHERE compliance_status = 'compliant') * 100.0 / NULLIF(COUNT(*), 0) AS compliance_rate,
    COUNT(*) FILTER (WHERE quality_score >= 8) * 100.0 / NULLIF(COUNT(*) FILTER (WHERE quality_score IS NOT NULL), 0) AS high_quality_rate
FROM session_summaries
WHERE first_request_at >= CURRENT_DATE
GROUP BY tenant_id;

-- ============================================================
-- 6. 兼容视图 v_session_analytics：统一对外暴露 gw_session_id
--    （session_summaries.session_key 的值即 gw_session_id）
-- ============================================================
CREATE OR REPLACE VIEW v_session_analytics AS
SELECT
    ss.session_key AS gw_session_id,
    ss.*,
    sd.task_id       AS task_id,
    sd.status        AS session_status,
    sd.first_request_at AS dim_first_request_at,
    sd.last_active_at   AS dim_last_active_at,
    sd.closed_at        AS closed_at
FROM session_summaries ss
LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key;

-- ============================================================
-- 7. 更新注释（说明 session_key 实际承载 gw_session_id）
-- ============================================================
COMMENT ON TABLE session_dim IS '会话维度映射表：gw_session_id ↔ session_key/task_id，维护会话生命周期（active/closed/idle）';
COMMENT ON COLUMN session_dim.session_key IS '会话兼容键，值同 gw_session_id（用于关联 session_summaries.session_key）';
COMMENT ON COLUMN session_summaries.session_key IS '会话唯一标识，值 = request_logs.gw_session_id（由 350 迁移修正，原 310 注释有误）';

COMMIT;
