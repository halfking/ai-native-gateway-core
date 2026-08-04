--
-- Name: product_module_features product_module_features_module_key_feature_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_module_features
    ADD CONSTRAINT product_module_features_module_key_feature_key_key UNIQUE (module_key, feature_key);

