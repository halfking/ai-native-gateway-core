--
-- Name: model_integrity_events model_integrity_events_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_integrity_events_super_admin ON public.model_integrity_events USING ((current_setting('app.bypass_rls'::text, true) = 'true'::text)) WITH CHECK ((current_setting('app.bypass_rls'::text, true) = 'true'::text));

