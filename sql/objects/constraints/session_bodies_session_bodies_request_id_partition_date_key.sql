--
-- Name: session_bodies session_bodies_request_id_partition_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_bodies
    ADD CONSTRAINT session_bodies_request_id_partition_date_key UNIQUE (request_id, partition_date);

