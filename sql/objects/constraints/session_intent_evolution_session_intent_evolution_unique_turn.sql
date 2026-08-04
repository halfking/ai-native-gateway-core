--
-- Name: session_intent_evolution session_intent_evolution_unique_turn; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_intent_evolution
    ADD CONSTRAINT session_intent_evolution_unique_turn UNIQUE (session_id, turn_number);

