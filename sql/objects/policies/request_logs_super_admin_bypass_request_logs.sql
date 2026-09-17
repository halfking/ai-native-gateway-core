--
-- Name: request_logs request_logs_super_admin_bypass; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY request_logs_super_admin_bypass ON public.request_logs USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));