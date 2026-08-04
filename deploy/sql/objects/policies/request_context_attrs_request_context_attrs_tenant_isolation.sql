--
-- Name: request_context_attrs request_context_attrs_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY request_context_attrs_tenant_isolation ON public.request_context_attrs USING ((tenant_id = current_setting('app.current_tenant'::text, true)));

