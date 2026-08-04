--
-- Name: approval_queue tenant_isolation_approval_queue; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_approval_queue ON public.approval_queue USING (((COALESCE(NULLIF(current_setting('app.current_role'::text, true), ''::text), ''::text) = 'super_admin'::text) OR (tenant_id = COALESCE(NULLIF(current_setting('app.current_tenant'::text, true), ''::text), 'default'::text)))) WITH CHECK (((COALESCE(NULLIF(current_setting('app.current_role'::text, true), ''::text), ''::text) = 'super_admin'::text) OR (tenant_id = COALESCE(NULLIF(current_setting('app.current_tenant'::text, true), ''::text), 'default'::text))));

