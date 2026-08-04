--
-- Name: offline_activation_requests offline_activation_requests_request_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.offline_activation_requests
    ADD CONSTRAINT offline_activation_requests_request_id_key UNIQUE (request_id);

