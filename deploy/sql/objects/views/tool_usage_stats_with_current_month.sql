--
-- Name: tool_usage_stats_with_current_month; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.tool_usage_stats_with_current_month AS
 SELECT tool_usage_stats_hot.id,
    tool_usage_stats_hot.tool_id,
    tool_usage_stats_hot.tenant_id,
    tool_usage_stats_hot.usage_date,
    tool_usage_stats_hot.call_count,
    tool_usage_stats_hot.success_count,
    tool_usage_stats_hot.error_count,
    tool_usage_stats_hot.avg_latency_ms,
    tool_usage_stats_hot.last_called_at,
    tool_usage_stats_hot.created_at,
    tool_usage_stats_hot.updated_at
   FROM public.tool_usage_stats_hot
UNION ALL
 SELECT tool_usage_stats.id,
    tool_usage_stats.tool_id,
    tool_usage_stats.tenant_id,
    tool_usage_stats.usage_date,
    tool_usage_stats.call_count,
    tool_usage_stats.success_count,
    tool_usage_stats.error_count,
    tool_usage_stats.avg_latency_ms,
    tool_usage_stats.last_called_at,
    tool_usage_stats.created_at,
    tool_usage_stats.updated_at
   FROM public.tool_usage_stats;

