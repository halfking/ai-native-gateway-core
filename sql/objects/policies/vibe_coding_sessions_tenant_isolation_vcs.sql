--
-- Name: vibe_coding_sessions tenant_isolation_vcs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_vcs ON public.vibe_coding_sessions USING ((tenant_id = public.get_current_tenant()));

