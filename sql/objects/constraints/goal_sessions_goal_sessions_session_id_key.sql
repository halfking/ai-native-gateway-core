--
-- Name: goal_sessions goal_sessions_session_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.goal_sessions
    ADD CONSTRAINT goal_sessions_session_id_key UNIQUE (session_id);

