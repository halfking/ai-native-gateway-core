--
-- Name: severity_action_matrix severity_matrix_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY severity_matrix_tenant ON public.severity_action_matrix USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

