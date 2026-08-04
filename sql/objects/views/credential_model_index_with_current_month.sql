--
-- Name: credential_model_index_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.credential_model_index_with_current_month AS
 SELECT credential_model_index_hot.bucket,
    credential_model_index_hot.credential_id,
    credential_model_index_hot.raw_model,
    credential_model_index_hot.canonical_id,
    credential_model_index_hot.billing_mode,
    credential_model_index_hot.unit_price_in_per_1m,
    credential_model_index_hot.unit_price_out_per_1m,
    credential_model_index_hot.context_window,
    credential_model_index_hot.success_rate,
    credential_model_index_hot.p95_latency_ms,
    credential_model_index_hot.active_sessions,
    credential_model_index_hot.concurrency_limit,
    credential_model_index_hot.pressure_ratio,
    credential_model_index_hot.score_smart,
    credential_model_index_hot.score_speed_first,
    credential_model_index_hot.score_cost_first,
    credential_model_index_hot.updated_at
   FROM public.credential_model_index_hot
UNION ALL
 SELECT credential_model_index.bucket,
    credential_model_index.credential_id,
    credential_model_index.raw_model,
    credential_model_index.canonical_id,
    credential_model_index.billing_mode,
    credential_model_index.unit_price_in_per_1m,
    credential_model_index.unit_price_out_per_1m,
    credential_model_index.context_window,
    credential_model_index.success_rate,
    credential_model_index.p95_latency_ms,
    credential_model_index.active_sessions,
    credential_model_index.concurrency_limit,
    credential_model_index.pressure_ratio,
    credential_model_index.score_smart,
    credential_model_index.score_speed_first,
    credential_model_index.score_cost_first,
    credential_model_index.updated_at
   FROM public.credential_model_index;

