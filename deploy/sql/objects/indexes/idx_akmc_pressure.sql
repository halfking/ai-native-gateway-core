--
-- Name: idx_akmc_pressure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_pressure ON public.api_key_model_cost USING btree (api_key_id, bucket DESC, pressure_ratio DESC);

