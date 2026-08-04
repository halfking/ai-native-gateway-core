--
-- Name: request_logs_archive tenant_isolation_request_logs_archive; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_request_logs_archive ON public.request_logs_archive USING ((tenant_id = public.get_current_tenant()));

