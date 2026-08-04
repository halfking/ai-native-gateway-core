--
-- Name: session_turns_2026_08 session_turns_2026_08_session_id_turn_no_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns_2026_08
    ADD CONSTRAINT session_turns_2026_08_session_id_turn_no_partition_date_key UNIQUE (session_id, turn_no, partition_date);

