--
-- Name: request_stats_error_drill_minute request_stats_error_drill_minute_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_stats_error_drill_minute
    ADD CONSTRAINT request_stats_error_drill_minute_pkey PRIMARY KEY (bucket, tenant_id, error_kind, model_name, provider_id, client_profile);

