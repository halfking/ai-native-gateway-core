--
-- Name: tenant_tool_policies tenant_isolation_tenant_tool_policies; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies USING (((tenant_id)::text = public.get_current_tenant()));

