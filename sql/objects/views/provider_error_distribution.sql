--
-- Name: provider_error_distribution; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.provider_error_distribution AS
 SELECT provider_id,
    error_type,
    error_code,
    count(*) AS error_count,
    sum(occurrences) AS total_occurrences,
    max(last_seen_at) AS last_occurrence,
    count(*) FILTER (WHERE (NOT resolved)) AS unresolved_count
   FROM public.provider_error_details
  WHERE (last_seen_at > (now() - '24:00:00'::interval))
  GROUP BY provider_id, error_type, error_code
  ORDER BY (sum(occurrences)) DESC;


--
-- Name: VIEW provider_error_distribution; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.provider_error_distribution IS '24小时错误分布统计 - 用于错误分析';


SET default_table_access_method = columnar;
