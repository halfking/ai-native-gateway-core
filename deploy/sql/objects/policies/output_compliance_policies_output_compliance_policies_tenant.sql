--
-- Name: output_compliance_policies output_compliance_policies_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY output_compliance_policies_tenant ON public.output_compliance_policies USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

