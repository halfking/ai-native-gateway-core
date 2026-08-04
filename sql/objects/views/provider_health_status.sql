--
-- Name: provider_health_status; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.provider_health_status AS
 SELECT p.id AS provider_id,
    p.display_name AS provider_name,
    pqp.model_name,
    pqp.quality_score,
    pqp.quality_grade,
    pqp.success_rate_5m,
    pqp.error_rate_5xx_5m,
    pqp.latency_p95_5m,
    pqp.availability_24h,
    pqp.consecutive_failures,
        CASE
            WHEN (pqp.consecutive_failures >= 5) THEN 'circuit_open'::text
            WHEN (pqp.quality_score >= (90)::numeric) THEN 'healthy'::text
            WHEN (pqp.quality_score >= (70)::numeric) THEN 'degraded'::text
            ELSE 'critical'::text
        END AS health_status,
    pqp.updated_at
   FROM (public.providers p
     JOIN public.provider_quality_profiles pqp ON ((p.id = pqp.provider_id)))
  WHERE (pqp.model_name IS NULL)
  ORDER BY pqp.quality_score DESC;


--
-- Name: VIEW provider_health_status; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.provider_health_status IS '供应商健康状态概览 - 用于监控看板';

