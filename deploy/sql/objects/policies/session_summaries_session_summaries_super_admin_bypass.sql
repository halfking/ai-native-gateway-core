--
-- Name: session_summaries session_summaries_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY session_summaries_super_admin_bypass ON public.session_summaries USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

