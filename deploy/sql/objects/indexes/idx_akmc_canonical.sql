--
-- Name: idx_akmc_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_canonical ON public.api_key_model_cost USING btree (canonical_id, bucket DESC);

