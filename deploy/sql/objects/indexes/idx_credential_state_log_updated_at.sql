--
-- Name: idx_credential_state_log_updated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_state_log_updated_at ON public.credential_state_log USING btree (updated_at DESC);

