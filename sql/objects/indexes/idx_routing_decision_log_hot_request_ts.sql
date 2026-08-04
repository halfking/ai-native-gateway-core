--
-- Name: idx_routing_decision_log_hot_request_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_routing_decision_log_hot_request_ts ON public.routing_decision_log_hot USING btree (request_id, ts);

