--
-- Name: output_compliance_custom_keywords output_compliance_custom_keywords_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY output_compliance_custom_keywords_super_admin ON public.output_compliance_custom_keywords USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

