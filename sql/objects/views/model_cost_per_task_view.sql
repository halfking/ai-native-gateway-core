--
-- Name: model_cost_per_task_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_cost_per_task_view AS
 SELECT canonical_id,
    raw_model,
    sum(cost_usd) AS total_cost_usd,
    sum((tokens_input + tokens_output)) AS total_tokens,
        CASE
            WHEN (sum((tokens_input + tokens_output)) > (0)::numeric) THEN ((sum(cost_usd) / sum((tokens_input + tokens_output))) * (1000000)::numeric)
            ELSE (0)::numeric
        END AS avg_cost_per_1m_usd,
        CASE
            WHEN (sum(requests_total) > 0) THEN ((sum(requests_success))::numeric / (sum(requests_total))::numeric)
            ELSE (0)::numeric
        END AS success_rate,
    ( SELECT avg(rl.latency_ms) AS avg
           FROM public.request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(requests_total) AS total_requests,
    count(DISTINCT api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (bucket >= (now() - '7 days'::interval))
  GROUP BY canonical_id, raw_model;


--
-- Name: VIEW model_cost_per_task_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';

