--
-- Name: credential_model_weekly_peak credential_model_weekly_peak_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_weekly_peak
    ADD CONSTRAINT credential_model_weekly_peak_pkey PRIMARY KEY (week_start, credential_id, raw_model);

