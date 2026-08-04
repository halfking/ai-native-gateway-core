--
-- Name: vibe_coding_projects tenant_isolation_vcp; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_vcp ON public.vibe_coding_projects USING ((tenant_id = public.get_current_tenant()));

