--
-- Name: idx_routing_decision_log_hot_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_hot_ts ON public.routing_decision_log_hot USING btree (ts DESC);

