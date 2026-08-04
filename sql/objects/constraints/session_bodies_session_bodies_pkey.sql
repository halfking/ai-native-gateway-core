--
-- Name: session_bodies session_bodies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies
    ADD CONSTRAINT session_bodies_pkey PRIMARY KEY (id, partition_date);

