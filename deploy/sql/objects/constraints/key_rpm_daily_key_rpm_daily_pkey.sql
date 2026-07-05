--
-- Name: key_rpm_daily key_rpm_daily_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.key_rpm_daily
    ADD CONSTRAINT key_rpm_daily_pkey PRIMARY KEY (api_key_id, day_bucket);

