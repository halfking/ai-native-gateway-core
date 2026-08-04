--
-- Name: idx_pmh_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmh_bucket ON public.provider_metrics_hour USING btree (bucket DESC);

