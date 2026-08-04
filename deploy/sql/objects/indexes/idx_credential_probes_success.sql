--
-- Name: idx_credential_probes_success; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probes_success ON public.credential_probes USING btree (success, created_at DESC);

