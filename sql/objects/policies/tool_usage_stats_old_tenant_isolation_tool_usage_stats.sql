--
-- Name: tool_usage_stats_old tenant_isolation_tool_usage_stats; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats_old USING (((tenant_id)::text = public.get_current_tenant()));

