--
-- Name: instance_heartbeats instance_heartbeats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.instance_heartbeats
    ADD CONSTRAINT instance_heartbeats_pkey PRIMARY KEY (instance_id, "timestamp");

