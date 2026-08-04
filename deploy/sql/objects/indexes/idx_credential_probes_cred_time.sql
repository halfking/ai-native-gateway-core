--
-- Name: idx_credential_probes_cred_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probes_cred_time ON public.credential_probes USING btree (credential_id, created_at DESC);

