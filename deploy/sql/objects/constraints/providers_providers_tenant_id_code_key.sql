--
-- Name: providers providers_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_tenant_id_code_key UNIQUE (tenant_id, code);

