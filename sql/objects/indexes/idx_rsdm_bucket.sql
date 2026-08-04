--
-- Name: idx_rsdm_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rsdm_bucket ON public.request_stats_dim_minute USING btree (bucket DESC);

