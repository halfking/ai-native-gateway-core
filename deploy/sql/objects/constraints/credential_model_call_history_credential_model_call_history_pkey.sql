--
-- Name: credential_model_call_history credential_model_call_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_call_history
    ADD CONSTRAINT credential_model_call_history_pkey PRIMARY KEY (credential_id, raw_model, window_start);

