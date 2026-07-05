--
-- Name: tenant_settings_kv tenant_settings_kv_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_settings_kv
    ADD CONSTRAINT tenant_settings_kv_pkey PRIMARY KEY (tenant_id, key);

