--
-- Name: idx_auto_tune_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auto_tune_cred ON public.auto_tune_audit USING btree (credential_id, created_at DESC);

