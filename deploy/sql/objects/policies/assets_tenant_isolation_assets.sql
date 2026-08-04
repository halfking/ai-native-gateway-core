--
-- Name: assets tenant_isolation_assets; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_assets ON public.assets USING ((tenant_id = public.get_current_tenant()));

