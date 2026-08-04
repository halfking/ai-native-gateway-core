--
-- Name: routing_decision_log_hot_chosen_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_hot_chosen_credential_id_ts_idx ON public.routing_decision_log_hot USING btree (chosen_credential_id, ts DESC) WHERE (chosen_credential_id IS NOT NULL);

