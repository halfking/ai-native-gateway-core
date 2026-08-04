--
-- Name: session_turns_2026_07 session_turns_2026_07_session_id_turn_no_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns_2026_07
    ADD CONSTRAINT session_turns_2026_07_session_id_turn_no_partition_date_key UNIQUE (session_id, turn_no, partition_date);

