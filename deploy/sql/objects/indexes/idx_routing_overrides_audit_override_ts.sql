--
-- Name: idx_routing_overrides_audit_override_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_override_ts ON public.routing_overrides_audit USING btree (override_id, ts DESC) WHERE (override_id IS NOT NULL);

