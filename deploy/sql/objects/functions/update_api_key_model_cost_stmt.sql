--
-- Name: update_api_key_model_cost_stmt(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_api_key_model_cost_stmt() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec record;
    agg record;
    bucket_ts TIMESTAMPTZ;
    limit_val INT;
    sql_update TEXT;
    affected_rows INT;
BEGIN

    FOR agg IN
        SELECT 
            date_trunc('hour', t.ts) + (FLOOR(EXTRACT(minute FROM t.ts) / 5) * INTERVAL '5 minutes') as bucket_ts,
            t.api_key_id as key_id,
            t.canonical_id,
            COALESCE(t.outbound_model, t.client_model) as raw_model,
            count(*) as req_total,
            sum(CASE WHEN t.success THEN 1 ELSE 0 END) as req_success,
            sum(COALESCE(t.prompt_tokens, 0)) as tok_in,
            sum(COALESCE(t.completion_tokens, 0)) as tok_out,
            sum(COALESCE(t.cost_usd, 0)) as cost,
            max(t.ts) as last_ts
        FROM new_rows t
        WHERE t.api_key_id IS NOT NULL
        GROUP BY 1, 2, 3, 4
    LOOP
        SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
        FROM api_keys WHERE id = agg.key_id;

        INSERT INTO api_key_model_cost (
            bucket, api_key_id, canonical_id, raw_model, billing_mode,
            requests_total, requests_success,
            tokens_input, tokens_output, cost_usd,
            active_concurrent, concurrency_limit, pressure_ratio,
            last_request_at, updated_at
        ) VALUES (
            agg.bucket_ts, agg.key_id, agg.canonical_id, agg.raw_model,
            'token',
            agg.req_total, agg.req_success,
            agg.tok_in, agg.tok_out,
            agg.cost,
            0, limit_val,
            CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
            agg.last_ts, NOW()
        )
        ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
            requests_total    = api_key_model_cost.requests_total + EXCLUDED.requests_total,
            requests_success  = api_key_model_cost.requests_success + EXCLUDED.requests_success,
            tokens_input      = api_key_model_cost.tokens_input + EXCLUDED.tokens_input,
            tokens_output     = api_key_model_cost.tokens_output + EXCLUDED.tokens_output,
            cost_usd          = api_key_model_cost.cost_usd + EXCLUDED.cost_usd,
            concurrency_limit = EXCLUDED.concurrency_limit,
            pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                      THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                      ELSE 0 END,
            last_request_at   = EXCLUDED.last_request_at,
            updated_at        = NOW();
    END LOOP;

    RETURN NULL;
END;
$$;

