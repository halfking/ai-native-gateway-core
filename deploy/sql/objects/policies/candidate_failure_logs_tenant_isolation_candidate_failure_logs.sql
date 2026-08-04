--
-- Name: candidate_failure_logs tenant_isolation_candidate_failure_logs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_candidate_failure_logs ON public.candidate_failure_logs USING ((tenant_id = public.get_current_tenant()));

