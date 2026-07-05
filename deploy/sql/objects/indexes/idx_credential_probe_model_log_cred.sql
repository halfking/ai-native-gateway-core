--
-- Name: idx_credential_probe_model_log_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probe_model_log_cred ON public.credential_probe_model_log USING btree (credential_id, created_at DESC);

