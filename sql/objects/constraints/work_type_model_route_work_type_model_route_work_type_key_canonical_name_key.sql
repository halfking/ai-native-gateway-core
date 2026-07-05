--
-- Name: work_type_model_route work_type_model_route_work_type_key_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route
    ADD CONSTRAINT work_type_model_route_work_type_key_canonical_name_key UNIQUE (work_type_key, canonical_name);

