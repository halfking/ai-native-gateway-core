--
-- Name: session_turn_snapshots session_turn_snapshots_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_turn_snapshots_tenant_isolation ON public.session_turn_snapshots USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

