--
-- Name: output_compliance_custom_keywords unique_output_compliance_keyword; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.output_compliance_custom_keywords
    ADD CONSTRAINT unique_output_compliance_keyword UNIQUE (tenant_id, keyword);

