--
-- Name: request_stats_dim_minute request_stats_dim_minute_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_stats_dim_minute
    ADD CONSTRAINT request_stats_dim_minute_pkey PRIMARY KEY (bucket, tenant_id, dim_type, dim_key);

