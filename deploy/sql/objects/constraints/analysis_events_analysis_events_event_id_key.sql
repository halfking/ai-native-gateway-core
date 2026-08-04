--
-- Name: analysis_events analysis_events_event_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.analysis_events
    ADD CONSTRAINT analysis_events_event_id_key UNIQUE (event_id);

