--
-- Name: update_api_key_model_cost(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_api_key_model_cost() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    bucket_ts TIMESTAMPTZ;
    key_id INT;
    limit_val INT;
BEGIN
    -- 计算 5min bucket（向下取整）
    bucket_ts := date_trunc('hour', NEW.ts)
                  + (FLOOR(EXTRACT(minute FROM NEW.ts) / 5) * INTERVAL '5 minutes');
    key_id := NEW.api_key_id;
    IF key_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- 查找 api_key 的 rate_limit_rpm（作为该 key 的并发近似上限）
    -- 注意：api_keys 表没有 concurrency_limit 列（已在 realtime-trigger SQL 中确认）。
    -- 用 rate_limit_rpm / 10 作为近似（假设平均请求耗时 6 秒）。
    SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
    FROM api_keys WHERE id = key_id;

    -- 增量更新（注意：不在这里累加 active_concurrent，因为 AFTER INSERT 只能加不能减。
    -- active_concurrent 由 customer_cost_view 通过 JOIN request_logs 实时计算）
    INSERT INTO api_key_model_cost (
        bucket, api_key_id, canonical_id, raw_model, billing_mode,
        requests_total, requests_success,
        tokens_input, tokens_output, cost_usd,
        active_concurrent, concurrency_limit, pressure_ratio,
        last_request_at, updated_at
    ) VALUES (
        bucket_ts, key_id, NEW.canonical_id, COALESCE(NEW.outbound_model, NEW.client_model),
        'token',
        1, CASE WHEN NEW.success THEN 1 ELSE 0 END,
        COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        COALESCE(NEW.cost_usd, 0),
        1, limit_val,
        CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
        NEW.ts, NOW()
    )
    ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
        requests_total    = api_key_model_cost.requests_total + 1,
        requests_success  = api_key_model_cost.requests_success + CASE WHEN NEW.success THEN 1 ELSE 0 END,
        tokens_input      = api_key_model_cost.tokens_input + COALESCE(NEW.prompt_tokens, 0),
        tokens_output     = api_key_model_cost.tokens_output + COALESCE(NEW.completion_tokens, 0),
        cost_usd          = api_key_model_cost.cost_usd + COALESCE(NEW.cost_usd, 0),
        -- active_concurrent 在 trigger 中不更新（只在视图层动态计算）
        concurrency_limit = EXCLUDED.concurrency_limit,
        pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                  THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                  ELSE 0 END,
        last_request_at   = NEW.ts,
        updated_at        = NOW();

    RETURN NEW;
END;
$$;

