--
-- Name: response_format_anomalies response_format_anomalies_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY response_format_anomalies_super_admin ON public.response_format_anomalies USING ((current_setting('app.bypass_rls'::text, true) = 'true'::text)) WITH CHECK ((current_setting('app.bypass_rls'::text, true) = 'true'::text));

