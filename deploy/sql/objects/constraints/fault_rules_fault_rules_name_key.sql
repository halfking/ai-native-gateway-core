--
-- Name: fault_rules fault_rules_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fault_rules
    ADD CONSTRAINT fault_rules_name_key UNIQUE (name);

