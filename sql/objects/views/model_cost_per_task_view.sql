--
-- Name: model_cost_per_task_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_cost_per_task_view AS
 SELECT mcp.canonical_id,
    mcp.raw_model,
    sum(mcp.cost_usd) AS total_cost_usd,
    sum((mcp.tokens_input + mcp.tokens_output)) AS total_tokens,
        CASE
            WHEN (sum((mcp.tokens_input + mcp.tokens_output)) > (0)::numeric) THEN ((sum(mcp.cost_usd) / sum((mcp.tokens_input + mcp.tokens_output))) * (1000000)::numeric)
            ELSE (0)::numeric
        END AS avg_cost_per_1m_usd,
        CASE
            WHEN (sum(mcp.requests_total) > 0) THEN ((sum(mcp.requests_success))::numeric / (sum(mcp.requests_total))::numeric)
            ELSE (0)::numeric
        END AS success_rate,
    ( SELECT avg(rl.latency_ms) AS avg
           FROM public.request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(mcp.requests_total) AS total_requests,
    count(DISTINCT mcp.api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (mcp.bucket >= (now() - '7 days'::interval))
  GROUP BY mcp.canonical_id, mcp.raw_model;


--
-- Name: VIEW model_cost_per_task_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';

