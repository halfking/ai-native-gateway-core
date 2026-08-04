--
-- Name: session_turn_snapshots uq_session_turn_snapshot; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_turn_snapshots
    ADD CONSTRAINT uq_session_turn_snapshot UNIQUE (tenant_id, gw_session_id, turn_no);

