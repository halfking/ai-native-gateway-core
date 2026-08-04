--
-- Name: injection_attack_vectors attack_vectors_tenant; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY attack_vectors_tenant ON public.injection_attack_vectors USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));

