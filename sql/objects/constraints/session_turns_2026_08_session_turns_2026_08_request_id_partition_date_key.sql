--
-- Name: session_turns_2026_08 session_turns_2026_08_request_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns_2026_08
    ADD CONSTRAINT session_turns_2026_08_request_id_partition_date_key UNIQUE (request_id, partition_date);

