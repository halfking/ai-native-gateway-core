--
-- Name: routing_decision_log routing_decision_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_decision_log
    ADD CONSTRAINT routing_decision_log_pkey PRIMARY KEY (ts, request_id);

