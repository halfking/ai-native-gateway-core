--
-- Name: instance_status_reports instance_status_reports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.instance_status_reports
    ADD CONSTRAINT instance_status_reports_pkey PRIMARY KEY (instance_id, "timestamp");

