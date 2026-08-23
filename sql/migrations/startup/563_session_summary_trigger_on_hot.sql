-- 563_session_summary_trigger_on_hot.sql
-- 修复 session_summaries.request_count / cost 写路径断链（2026-08-23）。
--
-- 根因（154 实测）：
--   1. 实时写入落 request_logs_hot（migration 341），但 trg_update_session_summary
--      仅曾挂在父表 request_logs，且 154 上触发器已缺失。
--   2. update_session_summary() 仍是 310 旧体（session_key/created_at/total_cost），
--      与真实列 gw_session_id/ts/cost_usd 不匹配。
--   3. summarystore 只写 title/summary，计数默认 0 → KPI 全 0。
--
-- 本迁移：
--   - 用 350/358 正确列名重写函数（仅聚合 session_summaries；session_dim 由
--     sessionv2mirror 维护；session_owners 在 154 不存在，跳过以免回滚热写入）。
--   - 触发器挂到 request_logs_hot；父表不挂，避免 promote 二次累加。
--   - 幂等回填：按 hot 聚合 REPLACE 计数/token/cost（可重复执行不翻倍）。
--
-- Idempotent: YES

BEGIN;

-- 确保 array_unique_append 存在（310 引入；缺则补）
CREATE OR REPLACE FUNCTION array_unique_append(arr TEXT[], new_elem TEXT)
RETURNS TEXT[] AS $$
BEGIN
    IF new_elem IS NULL THEN
        RETURN arr;
    END IF;
    IF new_elem = ANY(arr) THEN
        RETURN arr;
    END IF;
    RETURN array_append(arr, new_elem);
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
DECLARE
    v_prompt_tokens     BIGINT;
    v_completion_tokens BIGINT;
    v_total_tokens      BIGINT;
    v_cost              DECIMAL(14,8);
    v_input_cost        DECIMAL(12,6);
    v_output_cost       DECIMAL(12,6);
    v_latency_ms        INT;
    v_is_success        BOOLEAN;
    v_client_model      VARCHAR(100);
    v_upstream_model    VARCHAR(100);
    v_work_type         VARCHAR(50);
    v_provider_code     VARCHAR(100);
    v_gw_session_id     VARCHAR(128);
    v_token_ratio       DECIMAL(10,6);
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
    v_is_success        := COALESCE(NEW.success, false);
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

    BEGIN
        SELECT p.code INTO v_provider_code
        FROM providers p
        WHERE p.id = NEW.provider_id
        LIMIT 1;
    EXCEPTION WHEN OTHERS THEN
        v_provider_code := NULL;
    END;

    INSERT INTO session_summaries (
        session_key, tenant_id,
        first_request_at, last_request_at,
        request_count, success_count, error_count,
        total_cost_usd, input_cost_usd, output_cost_usd,
        total_prompt_tokens, total_completion_tokens,
        avg_latency_ms, min_latency_ms, max_latency_ms,
        models_used, work_types, providers, client_models,
        updated_at
    ) VALUES (
        v_gw_session_id, NEW.tenant_id,
        NEW.ts, NEW.ts,
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
        success_count = session_summaries.success_count
            + CASE WHEN v_is_success THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count
            + CASE WHEN v_is_success THEN 0 ELSE 1 END,
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
        primary_model = COALESCE(
            session_summaries.primary_model,
            v_upstream_model
        ),
        updated_at = NOW();

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 父表触发器：删除以免 promote INSERT 二次累加（聚合在 hot 完成）
DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs;

DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs_hot;
CREATE TRIGGER trg_update_session_summary
    AFTER INSERT ON request_logs_hot
    FOR EACH ROW
    WHEN (NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id <> '')
    EXECUTE FUNCTION update_session_summary();

COMMENT ON TRIGGER trg_update_session_summary ON request_logs_hot IS
'563: 实时聚合 session_summaries 计数/成本/token；仅挂 hot，避免 promote 双计。';

-- 幂等回填：用 hot 聚合 REPLACE 覆盖（可重复跑）
INSERT INTO session_summaries (
    session_key, tenant_id,
    first_request_at, last_request_at,
    request_count, success_count, error_count,
    total_cost_usd, input_cost_usd, output_cost_usd,
    total_prompt_tokens, total_completion_tokens,
    avg_latency_ms, min_latency_ms, max_latency_ms,
    models_used, work_types, providers, client_models,
    primary_model, updated_at
)
SELECT
    h.gw_session_id,
    h.tenant_id,
    MIN(h.ts),
    MAX(h.ts),
    COUNT(*)::INT,
    COUNT(*) FILTER (WHERE COALESCE(h.success, false))::INT,
    COUNT(*) FILTER (WHERE NOT COALESCE(h.success, false))::INT,
    COALESCE(SUM(h.cost_usd), 0)::NUMERIC(12,6),
    0::NUMERIC(12,6),
    0::NUMERIC(12,6),
    COALESCE(SUM(h.prompt_tokens), 0)::BIGINT,
    COALESCE(SUM(h.completion_tokens), 0)::BIGINT,
    COALESCE(AVG(h.latency_ms), 0)::INT,
    MIN(h.latency_ms),
    MAX(h.latency_ms),
    COALESCE(
        ARRAY(SELECT DISTINCT m FROM unnest(array_agg(h.outbound_model)) AS m WHERE m IS NOT NULL),
        '{}'::TEXT[]
    ),
    COALESCE(
        ARRAY(SELECT DISTINCT m FROM unnest(array_agg(h.work_type)) AS m WHERE m IS NOT NULL),
        '{}'::TEXT[]
    ),
    '{}'::TEXT[],
    COALESCE(
        ARRAY(SELECT DISTINCT m FROM unnest(array_agg(h.client_model)) AS m WHERE m IS NOT NULL),
        '{}'::TEXT[]
    ),
    (ARRAY_AGG(h.outbound_model ORDER BY h.ts) FILTER (WHERE h.outbound_model IS NOT NULL))[1],
    NOW()
FROM request_logs_hot h
WHERE h.gw_session_id IS NOT NULL AND h.gw_session_id <> ''
GROUP BY h.gw_session_id, h.tenant_id
ON CONFLICT (session_key) DO UPDATE SET
    first_request_at = LEAST(session_summaries.first_request_at, EXCLUDED.first_request_at),
    last_request_at = GREATEST(session_summaries.last_request_at, EXCLUDED.last_request_at),
    request_count = EXCLUDED.request_count,
    success_count = EXCLUDED.success_count,
    error_count = EXCLUDED.error_count,
    total_cost_usd = EXCLUDED.total_cost_usd,
    total_prompt_tokens = EXCLUDED.total_prompt_tokens,
    total_completion_tokens = EXCLUDED.total_completion_tokens,
    avg_latency_ms = EXCLUDED.avg_latency_ms,
    min_latency_ms = EXCLUDED.min_latency_ms,
    max_latency_ms = EXCLUDED.max_latency_ms,
    models_used = EXCLUDED.models_used,
    work_types = EXCLUDED.work_types,
    client_models = EXCLUDED.client_models,
    primary_model = COALESCE(EXCLUDED.primary_model, session_summaries.primary_model),
    updated_at = NOW();

COMMIT;
