--
-- Name: analysis_events tenant_isolation_analysis_events; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_analysis_events ON public.analysis_events USING ((tenant_id = public.get_current_tenant()));

