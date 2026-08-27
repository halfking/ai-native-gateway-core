--
-- Name: update_session_summary(); Type: FUNCTION; Schema: public; Owner: -
--
-- Canonical body aligned with migration 563 (hot-path columns: gw_session_id,
-- ts, cost_usd, outbound_model, success). Do NOT revert to the 310 shape
-- (session_key/created_at/total_cost) — that breaks live aggregation.

CREATE OR REPLACE FUNCTION public.update_session_summary() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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
        v_token_ratio := v_prompt_tokens::numeric / v_total_tokens::numeric;
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
        primary_model = COALESCE(session_summaries.primary_model, v_upstream_model),
        updated_at = NOW();

    RETURN NEW;
END;
$$;
