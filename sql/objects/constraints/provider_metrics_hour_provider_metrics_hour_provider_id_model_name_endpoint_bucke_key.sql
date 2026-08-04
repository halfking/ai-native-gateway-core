--
-- Name: provider_metrics_hour provider_metrics_hour_provider_id_model_name_endpoint_bucke_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_metrics_hour
    ADD CONSTRAINT provider_metrics_hour_provider_id_model_name_endpoint_bucke_key UNIQUE (provider_id, model_name, endpoint, bucket);

