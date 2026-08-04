--
-- Name: sessions_2026_08 sessions_2026_08_session_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions_2026_08
    ADD CONSTRAINT sessions_2026_08_session_id_partition_date_key UNIQUE (session_id, partition_date);

