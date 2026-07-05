--
-- Name: provider_quality_rollup provider_quality_rollup_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_rollup
    ADD CONSTRAINT provider_quality_rollup_pkey PRIMARY KEY (provider_id, bucket_start);

