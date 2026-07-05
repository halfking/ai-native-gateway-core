--
-- Name: idx_security_audit_log_internal_svc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_internal_svc ON public.security_audit_log USING btree (internal_service_id, ts DESC) WHERE (internal_service_id IS NOT NULL);

