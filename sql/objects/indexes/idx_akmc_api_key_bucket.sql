--
-- Name: idx_akmc_api_key_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_api_key_bucket ON public.api_key_model_cost USING btree (api_key_id, bucket DESC);

