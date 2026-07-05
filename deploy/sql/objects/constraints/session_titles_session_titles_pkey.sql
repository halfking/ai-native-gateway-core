--
-- Name: session_titles session_titles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_titles
    ADD CONSTRAINT session_titles_pkey PRIMARY KEY (task_id, scoped_session_id);

