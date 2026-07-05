--
-- Name: credential_model_index credential_model_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_index
    ADD CONSTRAINT credential_model_index_pkey PRIMARY KEY (bucket, credential_id, raw_model);

