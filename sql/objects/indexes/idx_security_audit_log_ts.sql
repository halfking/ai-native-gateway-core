--
-- Name: idx_security_audit_log_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_ts ON public.security_audit_log USING btree (ts DESC);

