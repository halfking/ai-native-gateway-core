--
-- Name: session_turns session_turns_request_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turns
    ADD CONSTRAINT session_turns_request_id_partition_date_key UNIQUE (request_id, partition_date);

