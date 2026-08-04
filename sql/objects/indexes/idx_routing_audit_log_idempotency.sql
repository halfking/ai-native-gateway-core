--
-- Name: idx_routing_audit_log_idempotency; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_routing_audit_log_idempotency ON public.routing_audit_log USING btree (idempotency_key);

