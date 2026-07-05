--
-- Name: idx_provider_models_standardized; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_standardized ON public.provider_models USING btree (standardized_name) WHERE (standardized_name IS NOT NULL);

