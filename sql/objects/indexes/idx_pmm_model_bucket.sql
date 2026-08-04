--
-- Name: idx_pmm_model_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmm_model_bucket ON public.provider_metrics_minute USING btree (provider_id, model_name, bucket DESC);

