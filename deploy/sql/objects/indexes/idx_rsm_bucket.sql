--
-- Name: idx_rsm_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rsm_bucket ON public.request_stats_minute USING btree (bucket DESC);

