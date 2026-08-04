--
-- Name: idx_pmh_provider_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmh_provider_bucket ON public.provider_metrics_hour USING btree (provider_id, bucket DESC);

