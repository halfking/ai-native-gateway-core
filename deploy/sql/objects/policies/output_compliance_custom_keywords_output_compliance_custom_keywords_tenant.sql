--
-- Name: output_compliance_custom_keywords output_compliance_custom_keywords_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY output_compliance_custom_keywords_tenant ON public.output_compliance_custom_keywords USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

