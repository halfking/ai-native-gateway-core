--
-- Name: node_probe_state node_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.node_probe_state
    ADD CONSTRAINT node_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name);

