--
-- Name: idx_routing_audit_log_action; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_action ON public.routing_audit_log USING btree (action, ts DESC);

