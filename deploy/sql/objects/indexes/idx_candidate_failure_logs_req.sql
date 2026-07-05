--
-- Name: idx_candidate_failure_logs_req; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_req ON public.candidate_failure_logs USING btree (request_id);

