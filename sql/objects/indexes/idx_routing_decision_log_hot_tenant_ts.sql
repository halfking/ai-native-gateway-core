--
-- Name: idx_routing_decision_log_hot_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_hot_tenant_ts ON public.routing_decision_log_hot USING btree (tenant_id, ts DESC);

