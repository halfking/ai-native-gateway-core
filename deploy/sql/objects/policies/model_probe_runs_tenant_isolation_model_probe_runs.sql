--
-- Name: model_probe_runs tenant_isolation_model_probe_runs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_model_probe_runs ON public.model_probe_runs USING ((tenant_id = public.get_current_tenant()));

