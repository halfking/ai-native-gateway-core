--
-- Name: credential_state_log credential_state_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_state_log
    ADD CONSTRAINT credential_state_log_pkey PRIMARY KEY (credential_id, raw_model_name);

