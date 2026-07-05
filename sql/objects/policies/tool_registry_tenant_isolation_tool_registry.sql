--
-- Name: tool_registry tenant_isolation_tool_registry; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry USING ((((tenant_id)::text = public.get_current_tenant()) OR (tenant_id IS NULL) OR ((tenant_id)::text = 'default'::text)));

