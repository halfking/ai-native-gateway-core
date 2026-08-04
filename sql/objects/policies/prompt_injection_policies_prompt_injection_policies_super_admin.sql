--
-- Name: prompt_injection_policies prompt_injection_policies_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY prompt_injection_policies_super_admin ON public.prompt_injection_policies USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

