--
-- Name: idx_routing_decision_log_identity_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_identity_hash ON public.routing_decision_log USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);

