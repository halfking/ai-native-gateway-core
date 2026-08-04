--
-- Name: output_compliance_policies output_compliance_policies_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY output_compliance_policies_super_admin ON public.output_compliance_policies USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

