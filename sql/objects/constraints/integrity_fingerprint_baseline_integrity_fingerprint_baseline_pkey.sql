--
-- Name: integrity_fingerprint_baseline integrity_fingerprint_baseline_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.integrity_fingerprint_baseline
    ADD CONSTRAINT integrity_fingerprint_baseline_pkey PRIMARY KEY (tenant_id, credential_id, raw_model_name);

