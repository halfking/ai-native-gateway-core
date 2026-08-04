--
-- Name: severity_action_matrix severity_matrix_super_admin; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY severity_matrix_super_admin ON public.severity_action_matrix USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));

