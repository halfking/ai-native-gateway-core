--
-- Name: uq_routing_audit_log_idem; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_routing_audit_log_idem ON public.routing_audit_log USING btree (idempotency_key);

