--
-- Name: idx_routing_audit_log_incident; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_incident ON public.routing_audit_log USING btree (incident_id);

