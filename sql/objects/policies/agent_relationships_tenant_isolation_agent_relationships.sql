--
-- Name: agent_relationships tenant_isolation_agent_relationships; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_agent_relationships ON public.agent_relationships USING (((EXISTS ( SELECT 1
   FROM public.agents a_src
  WHERE ((a_src.id = agent_relationships.src_agent_id) AND (a_src.tenant_id = public.get_current_tenant())))) AND (EXISTS ( SELECT 1
   FROM public.agents a_dst
  WHERE ((a_dst.id = agent_relationships.dst_agent_id) AND (a_dst.tenant_id = public.get_current_tenant()))))));

