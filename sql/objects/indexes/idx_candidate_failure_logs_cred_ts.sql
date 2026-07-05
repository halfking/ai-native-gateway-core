--
-- Name: idx_candidate_failure_logs_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_cred_ts ON public.candidate_failure_logs USING btree (credential_id, ts DESC);

