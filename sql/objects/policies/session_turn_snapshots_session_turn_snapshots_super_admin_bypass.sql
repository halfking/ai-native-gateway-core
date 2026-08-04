--
-- Name: session_turn_snapshots session_turn_snapshots_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_turn_snapshots_super_admin_bypass ON public.session_turn_snapshots USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

