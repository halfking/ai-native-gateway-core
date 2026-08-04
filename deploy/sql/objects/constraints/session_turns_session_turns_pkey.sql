--
-- Name: session_turns session_turns_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns
    ADD CONSTRAINT session_turns_pkey PRIMARY KEY (id, partition_date);

