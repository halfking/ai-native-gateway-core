--
-- Name: session_bodies_2026_07 session_bodies_2026_07_session_id_turn_no_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies_2026_07
    ADD CONSTRAINT session_bodies_2026_07_session_id_turn_no_partition_date_key UNIQUE (session_id, turn_no, partition_date);

