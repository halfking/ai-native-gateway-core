--
-- Name: routing_decision_log_hot_success_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_hot_success_ts_idx ON public.routing_decision_log_hot USING btree (success, ts DESC);

