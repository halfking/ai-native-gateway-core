--
-- Name: credential_model_stats_1m credential_model_stats_1m_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_stats_1m
    ADD CONSTRAINT credential_model_stats_1m_pkey PRIMARY KEY (bucket, credential_id, raw_model);

