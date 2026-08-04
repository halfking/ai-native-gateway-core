--
-- Name: injection_attack_vectors unique_attack_hash; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.injection_attack_vectors
    ADD CONSTRAINT unique_attack_hash UNIQUE (tenant_id, attack_hash);

