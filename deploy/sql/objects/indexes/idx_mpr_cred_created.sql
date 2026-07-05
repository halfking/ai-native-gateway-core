--
-- Name: idx_mpr_cred_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_cred_created ON public.model_probe_runs USING btree (credential_id, created_at DESC);

