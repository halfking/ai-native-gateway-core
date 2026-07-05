--
-- Name: tenant_model_policies tenant_model_policies_tenant_id_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies
    ADD CONSTRAINT tenant_model_policies_tenant_id_canonical_name_key UNIQUE (tenant_id, canonical_name);

