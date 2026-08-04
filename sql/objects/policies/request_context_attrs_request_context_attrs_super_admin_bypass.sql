--
-- Name: request_context_attrs request_context_attrs_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY request_context_attrs_super_admin_bypass ON public.request_context_attrs USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

