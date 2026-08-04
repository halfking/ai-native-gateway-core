--
-- Name: download_events download_events_request_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.download_events
    ADD CONSTRAINT download_events_request_id_key UNIQUE (request_id);

