--
-- Name: assets pk_assets; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.assets
    ADD CONSTRAINT pk_assets PRIMARY KEY (kind, ref_id);

