--
-- Name: idx_routing_decision_log_request_id_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_request_id_ts ON public.routing_decision_log USING btree (request_id, ts DESC);

