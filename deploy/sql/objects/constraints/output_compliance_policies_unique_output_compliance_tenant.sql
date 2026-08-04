--
-- Name: output_compliance_policies unique_output_compliance_tenant; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.output_compliance_policies
    ADD CONSTRAINT unique_output_compliance_tenant UNIQUE (tenant_id);

