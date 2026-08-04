--
-- Name: idx_pmm_provider_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmm_provider_bucket ON public.provider_metrics_minute USING btree (provider_id, bucket DESC);

