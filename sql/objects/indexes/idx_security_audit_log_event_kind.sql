--
-- Name: idx_security_audit_log_event_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_event_kind ON public.security_audit_log USING btree (event_kind, ts DESC);

