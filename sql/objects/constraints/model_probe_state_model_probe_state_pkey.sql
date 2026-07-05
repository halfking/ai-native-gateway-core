--
-- Name: model_probe_state model_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_state
    ADD CONSTRAINT model_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name);

