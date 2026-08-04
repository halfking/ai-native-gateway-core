--
-- Name: sessions_2026_07 sessions_2026_07_session_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions_2026_07
    ADD CONSTRAINT sessions_2026_07_session_id_partition_date_key UNIQUE (session_id, partition_date);

