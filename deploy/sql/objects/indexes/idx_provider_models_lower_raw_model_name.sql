--
-- Name: idx_provider_models_lower_raw_model_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_lower_raw_model_name ON public.provider_models USING btree (lower(raw_model_name));

