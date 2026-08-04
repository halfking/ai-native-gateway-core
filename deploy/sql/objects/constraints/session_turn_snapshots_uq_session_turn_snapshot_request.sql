--
-- Name: session_turn_snapshots uq_session_turn_snapshot_request; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turn_snapshots
    ADD CONSTRAINT uq_session_turn_snapshot_request UNIQUE (tenant_id, request_id);

