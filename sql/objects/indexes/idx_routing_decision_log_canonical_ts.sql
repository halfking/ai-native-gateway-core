--
-- Name: idx_routing_decision_log_canonical_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_canonical_ts ON public.routing_decision_log USING btree (canonical_model, ts DESC);

