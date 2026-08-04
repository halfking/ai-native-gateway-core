--
-- Name: idx_routing_decision_log_part_success; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_part_success ON ONLY public.routing_decision_log USING btree (success, ts DESC);

