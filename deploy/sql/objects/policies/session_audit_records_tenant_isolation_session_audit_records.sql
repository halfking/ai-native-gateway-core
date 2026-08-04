--
-- Name: session_audit_records tenant_isolation_session_audit_records; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_session_audit_records ON public.session_audit_records USING (((COALESCE(NULLIF(current_setting('app.current_role'::text, true), ''::text), ''::text) = 'super_admin'::text) OR (tenant_id = COALESCE(NULLIF(current_setting('app.current_tenant'::text, true), ''::text), 'default'::text)))) WITH CHECK (((COALESCE(NULLIF(current_setting('app.current_role'::text, true), ''::text), ''::text) = 'super_admin'::text) OR (tenant_id = COALESCE(NULLIF(current_setting('app.current_tenant'::text, true), ''::text), 'default'::text))));

