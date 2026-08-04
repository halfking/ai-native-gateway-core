--
-- Name: sessions sessions_session_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_session_id_partition_date_key UNIQUE (session_id, partition_date);

