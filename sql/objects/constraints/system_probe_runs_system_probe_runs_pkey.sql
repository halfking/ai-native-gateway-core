--
-- Name: system_probe_runs system_probe_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_probe_runs
    ADD CONSTRAINT system_probe_runs_pkey PRIMARY KEY (id, created_at);

