--
-- Name: request_wal request_wal_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal
    ADD CONSTRAINT request_wal_pkey PRIMARY KEY (request_id, created_at);

