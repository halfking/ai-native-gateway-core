--
-- Name: sessions sessions_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sessions_tenant_isolation ON public.sessions USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

