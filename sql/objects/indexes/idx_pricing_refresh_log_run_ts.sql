--
-- Name: idx_pricing_refresh_log_run_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_refresh_log_run_ts ON public.pricing_refresh_log USING btree (run_ts DESC);

