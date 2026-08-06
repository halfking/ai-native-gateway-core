--
-- Name: ursm_node_snapshot_min ursm_node_snapshot_min_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ursm_node_snapshot_min
    ADD CONSTRAINT ursm_node_snapshot_min_pkey PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name);

