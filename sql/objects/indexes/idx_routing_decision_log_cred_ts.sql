--
-- Name: idx_routing_decision_log_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_cred_ts ON public.routing_decision_log USING btree (chosen_credential_id, ts DESC);

