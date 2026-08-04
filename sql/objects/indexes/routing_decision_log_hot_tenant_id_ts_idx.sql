--
-- Name: routing_decision_log_hot_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_hot_tenant_id_ts_idx ON public.routing_decision_log_hot USING btree (tenant_id, ts DESC) WHERE (tenant_id IS NOT NULL);

