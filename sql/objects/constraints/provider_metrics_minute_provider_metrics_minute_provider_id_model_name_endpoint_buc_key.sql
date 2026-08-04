--
-- Name: provider_metrics_minute provider_metrics_minute_provider_id_model_name_endpoint_buc_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_metrics_minute
    ADD CONSTRAINT provider_metrics_minute_provider_id_model_name_endpoint_buc_key UNIQUE (provider_id, model_name, endpoint, bucket);

