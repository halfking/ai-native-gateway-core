--
-- Name: subscription_tiers subscription_tiers_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_tiers
    ADD CONSTRAINT subscription_tiers_code_key UNIQUE (code);

