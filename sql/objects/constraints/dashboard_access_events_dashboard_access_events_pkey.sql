--
-- Name: dashboard_access_events dashboard_access_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dashboard_access_events
    ADD CONSTRAINT dashboard_access_events_pkey PRIMARY KEY (event_id, created_at);

