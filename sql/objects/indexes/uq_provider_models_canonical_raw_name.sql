--
-- Name: uq_provider_models_canonical_raw_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_provider_models_canonical_raw_name ON public.provider_models USING btree (provider_id, canonical_raw_name, raw_model_name);

