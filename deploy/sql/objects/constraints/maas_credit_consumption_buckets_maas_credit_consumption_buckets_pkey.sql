--
-- Name: maas_credit_consumption_buckets maas_credit_consumption_buckets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maas_credit_consumption_buckets
    ADD CONSTRAINT maas_credit_consumption_buckets_pkey PRIMARY KEY (tenant_id, bucket_start);

