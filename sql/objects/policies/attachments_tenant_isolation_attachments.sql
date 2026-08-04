--
-- Name: attachments tenant_isolation_attachments; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_attachments ON public.attachments USING ((tenant_id = public.get_current_tenant()));

