--
-- Name: idx_routing_decision_log_part_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_part_tenant_ts ON ONLY public.routing_decision_log USING btree (tenant_id, ts DESC) WHERE (tenant_id IS NOT NULL);

