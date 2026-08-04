--
-- Name: session_turns_2026_07 session_turns_2026_07_request_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns_2026_07
    ADD CONSTRAINT session_turns_2026_07_request_id_partition_date_key UNIQUE (request_id, partition_date);

