--
-- Name: request_stats_minute request_stats_minute_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_stats_minute
    ADD CONSTRAINT request_stats_minute_pkey PRIMARY KEY (bucket, tenant_id, provider_id, canonical_id);

