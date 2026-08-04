--
-- Name: idx_integrity_fingerprint_baseline_cred_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_integrity_fingerprint_baseline_cred_model ON public.integrity_fingerprint_baseline USING btree (credential_id, raw_model_name);

