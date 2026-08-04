--
-- Name: session_bodies session_bodies_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_bodies_tenant_isolation ON public.session_bodies USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

