--
-- Name: idx_routing_audit_log_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_ts ON public.routing_audit_log USING btree (ts DESC);

