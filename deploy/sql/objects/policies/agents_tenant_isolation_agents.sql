--
-- Name: agents tenant_isolation_agents; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_agents ON public.agents USING ((tenant_id = public.get_current_tenant()));

