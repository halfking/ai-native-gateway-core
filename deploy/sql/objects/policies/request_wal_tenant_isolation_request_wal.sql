--
-- Name: request_wal tenant_isolation_request_wal; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_request_wal ON public.request_wal USING (((tenant_id)::text = public.get_current_tenant()));

