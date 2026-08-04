--
-- Name: idx_provider_models_lower_standardized_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_lower_standardized_name ON public.provider_models USING btree (lower(standardized_name));

