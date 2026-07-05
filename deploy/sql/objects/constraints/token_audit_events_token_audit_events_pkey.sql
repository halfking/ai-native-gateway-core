--
-- Name: token_audit_events token_audit_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_audit_events
    ADD CONSTRAINT token_audit_events_pkey PRIMARY KEY (id, ts);

