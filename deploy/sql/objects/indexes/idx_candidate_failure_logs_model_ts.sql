--
-- Name: idx_candidate_failure_logs_model_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_model_ts ON public.candidate_failure_logs USING btree (raw_model_name, ts DESC);

