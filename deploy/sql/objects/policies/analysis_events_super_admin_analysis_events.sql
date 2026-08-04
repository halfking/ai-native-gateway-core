--
-- Name: analysis_events super_admin_analysis_events; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY super_admin_analysis_events ON public.analysis_events USING ((current_setting('app.is_super_admin'::text, true) = 'true'::text));

