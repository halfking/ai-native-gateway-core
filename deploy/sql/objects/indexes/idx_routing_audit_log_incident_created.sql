--
-- Name: idx_routing_audit_log_incident_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_incident_created ON public.routing_audit_log USING btree (incident_id, created_at DESC) WHERE (incident_id IS NOT NULL);

