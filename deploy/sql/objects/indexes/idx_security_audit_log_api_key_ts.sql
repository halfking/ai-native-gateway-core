--
-- Name: idx_security_audit_log_api_key_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_api_key_ts ON public.security_audit_log USING btree (api_key_id, ts DESC) WHERE (api_key_id IS NOT NULL);

