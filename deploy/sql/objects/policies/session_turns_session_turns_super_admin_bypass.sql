--
-- Name: session_turns session_turns_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_turns_super_admin_bypass ON public.session_turns USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

