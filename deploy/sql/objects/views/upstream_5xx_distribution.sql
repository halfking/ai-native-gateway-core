--
-- Name: upstream_5xx_distribution; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.upstream_5xx_distribution AS
 SELECT http_status,
    failure_hint,
    count(*) AS error_count,
    count(DISTINCT request_id) AS affected_requests,
    array_agg(DISTINCT (details ->> 'credential_id'::text)) FILTER (WHERE (details ? 'credential_id'::text)) AS affected_credentials,
    array_agg(DISTINCT (details ->> 'raw_model'::text)) FILTER (WHERE (details ? 'raw_model'::text)) AS affected_models,
    min(event_timestamp) AS first_seen,
    max(event_timestamp) AS last_seen,
    array_agg(response_body) FILTER (WHERE (response_body IS NOT NULL)) AS sample_bodies
   FROM public.request_stage_events
  WHERE ((stage = 'upstream_request'::text) AND (status = 'failed'::text) AND (http_status >= 500) AND (event_timestamp >= (now() - '01:00:00'::interval)))
  GROUP BY http_status, failure_hint
  ORDER BY (count(*)) DESC;


--
-- Name: VIEW upstream_5xx_distribution; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.upstream_5xx_distribution IS '最近 1 小时上游 5xx 错误分布。按状态码和 failure_hint 分组，列出受影响的凭据和模型。';

