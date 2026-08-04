--
-- Name: pii_patterns pii_patterns_pattern_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pii_patterns
    ADD CONSTRAINT pii_patterns_pattern_name_key UNIQUE (pattern_name);

