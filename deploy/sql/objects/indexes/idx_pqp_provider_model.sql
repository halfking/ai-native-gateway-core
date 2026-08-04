--
-- Name: idx_pqp_provider_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pqp_provider_model ON public.provider_quality_profiles USING btree (provider_id, model_name) WHERE (model_name IS NOT NULL);

