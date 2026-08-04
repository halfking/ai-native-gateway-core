--
-- Name: vibe_code_reviews tenant_isolation_vcr; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_vcr ON public.vibe_code_reviews USING ((tenant_id = public.get_current_tenant()));

