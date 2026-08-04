--
-- Name: session_turns session_turns_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_turns_tenant_isolation ON public.session_turns USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

