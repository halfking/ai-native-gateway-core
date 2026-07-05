--
-- Name: idx_candidate_failure_logs_provider_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_provider_ts ON public.candidate_failure_logs USING btree (provider_id, ts DESC);

