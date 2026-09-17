--
-- Name: tenant_model_policies tenant_model_policies_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_model_policies_super_admin_bypass ON public.tenant_model_policies USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));