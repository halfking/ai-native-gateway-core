-- 883 down: 把两个活指标子查询还原成读 v1 母表（860 之后物理名是 `_request_logs`）。
--
-- 用途：**回滚 883 的读源切换**。注意这是「退回读一个已冻结的族」——
-- `active_concurrent` 会重新变成恒 0、`avg_latency_ms` 重新变成截止 2026-10-13
-- 停写那一刻的快照值。回滚只在「883 引入了别的问题」时才需要。
--
-- 列名 / 列序 / 列类型与 883 之前完全一致 ⇒ 可直接 CREATE OR REPLACE。
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

CREATE OR REPLACE VIEW public.customer_cost_view AS
 SELECT akmc.api_key_id,
    ak.key_alias,
    ak.tenant_id,
    ak.application_id,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_1h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '24:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_24h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '7 days'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_7d,
    sum(akmc.requests_total) AS total_auto_requests,
    sum(akmc.requests_success) AS total_auto_success,
    ( SELECT count(*) AS count
           FROM public._request_logs rl
          WHERE ((rl.api_key_id = akmc.api_key_id) AND (rl.is_auto_request = true) AND (rl.ts >= (now() - '00:05:00'::interval)) AND (rl.success IS NOT NULL) AND (rl.ts IS NOT NULL))) AS active_concurrent,
    max(akmc.concurrency_limit) AS concurrency_limit,
    avg(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.pressure_ratio
            ELSE NULL::numeric
        END) AS avg_pressure_1h,
    max(akmc.score_smart) AS best_score_smart,
    max(akmc.score_speed_first) AS best_score_speed_first,
    max(akmc.score_cost_first) AS best_score_cost_first,
    max(akmc.last_request_at) AS last_request_at
   FROM (public.api_key_model_cost akmc
     JOIN public.api_keys ak ON ((ak.id = akmc.api_key_id)))
  GROUP BY akmc.api_key_id, ak.key_alias, ak.tenant_id, ak.application_id;

COMMENT ON VIEW public.customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from request_logs (5min window).';

CREATE OR REPLACE VIEW public.model_cost_per_task_view AS
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
           FROM public._request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(requests_total) AS total_requests,
    count(DISTINCT api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (bucket >= (now() - '7 days'::interval))
  GROUP BY canonical_id, raw_model;

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';

COMMIT;
