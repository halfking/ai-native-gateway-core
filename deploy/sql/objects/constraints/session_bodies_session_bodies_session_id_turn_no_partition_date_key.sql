--
-- Name: session_bodies session_bodies_session_id_turn_no_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies
    ADD CONSTRAINT session_bodies_session_id_turn_no_partition_date_key UNIQUE (session_id, turn_no, partition_date);

