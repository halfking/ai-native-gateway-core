--
-- Name: tool_call_events tenant_isolation_tool_call_events; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events USING (((tenant_id)::text = public.get_current_tenant()));

