--
-- Name: analysis_events analysis_events_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY analysis_events_super_admin_bypass ON public.analysis_events USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

