-- Migration 358 Rollback: Session Ownership Modeling
-- 2026-07-07: 回滚会话归属建模

BEGIN;

-- 删除 session_owners 表
DROP TABLE IF EXISTS session_owners;

-- 删除 358 新增的物化视图（在删除 session_owners / session_dim 列之前，先删依赖它们的对象）
DROP MATERIALIZED VIEW IF EXISTS session_owner_stats;

-- 恢复 357 的 refresh 函数（不加 session_owner_stats）
CREATE OR REPLACE FUNCTION refresh_session_analytics_views()
RETURNS void AS $$
BEGIN
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_stats;
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_task_stats;
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_task_matrix;
END;
$$ LANGUAGE plpgsql;

-- 删除 session_dim 新增的 owner 列
ALTER TABLE session_dim
    DROP COLUMN IF EXISTS client_id,
    DROP COLUMN IF EXISTS end_user_id,
    DROP COLUMN IF EXISTS api_key_owner_user,
    DROP COLUMN IF EXISTS owner_user,
    DROP COLUMN IF EXISTS application_code,
    DROP COLUMN IF EXISTS application_id,
    DROP COLUMN IF EXISTS api_key_id;

-- 恢复 350 的 trigger 函数定义（不含 owner 传播与 session_owners 维护）
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
    v_gw_session_id := NEW.gw_session_id;
    IF v_gw_session_id IS NULL OR v_gw_session_id = '' THEN
        RETURN NEW;
    END IF;

    v_prompt_tokens     := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_total_tokens      := v_prompt_tokens + v_completion_tokens;
    v_cost              := COALESCE(NEW.cost_usd, 0);
    v_latency_ms        := COALESCE(NEW.latency_ms, 0);
    v_is_success        := NEW.success;
    v_client_model      := NEW.client_model;
    v_upstream_model    := NEW.outbound_model;
    v_work_type         := NEW.work_type;

    IF v_total_tokens > 0 THEN
        v_token_ratio := v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6);
    ELSE
        v_token_ratio := 0.5;
    END IF;
    v_input_cost  := (v_cost * v_token_ratio)::DECIMAL(12,6);
    v_output_cost := (v_cost - v_input_cost)::DECIMAL(12,6);

    SELECT p.code INTO v_provider_code
    FROM providers p
    WHERE p.id = NEW.provider_id
    LIMIT 1;

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

    INSERT INTO session_summaries (
        session_key, tenant_id, first_request_at, last_request_at,
        request_count, success_count, error_count,
        total_cost_usd, input_cost_usd, output_cost_usd,
        total_prompt_tokens, total_completion_tokens,
        avg_latency_ms, min_latency_ms, max_latency_ms,
        models_used, work_types, providers, client_models, updated_at
    ) VALUES (
        v_gw_session_id, NEW.tenant_id, NEW.ts, NEW.ts,
        1,
        CASE WHEN v_is_success THEN 1 ELSE 0 END,
        CASE WHEN v_is_success THEN 0 ELSE 1 END,
        v_cost, v_input_cost, v_output_cost,
        v_prompt_tokens, v_completion_tokens,
        v_latency_ms, v_latency_ms, v_latency_ms,
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

COMMIT;
