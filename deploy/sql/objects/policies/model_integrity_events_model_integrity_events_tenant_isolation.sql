--
-- Name: model_integrity_events model_integrity_events_tenant_isolation; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY model_integrity_events_tenant_isolation ON public.model_integrity_events USING (((tenant_id IS NULL) OR (tenant_id = public.get_current_tenant()))) WITH CHECK (((tenant_id IS NULL) OR (tenant_id = public.get_current_tenant())));

