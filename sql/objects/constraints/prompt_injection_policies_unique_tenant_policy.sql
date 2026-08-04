--
-- Name: prompt_injection_policies unique_tenant_policy; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prompt_injection_policies
    ADD CONSTRAINT unique_tenant_policy UNIQUE (tenant_id);

