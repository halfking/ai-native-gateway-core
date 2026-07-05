--
-- Name: idx_token_audit_events_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_token_audit_events_credential ON public.token_audit_events USING btree (credential_id, ts DESC);

