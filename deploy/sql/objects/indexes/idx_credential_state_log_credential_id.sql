--
-- Name: idx_credential_state_log_credential_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_state_log_credential_id ON public.credential_state_log USING btree (credential_id);

