--
-- Name: idx_pmm_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmm_bucket ON public.provider_metrics_minute USING btree (bucket DESC);

