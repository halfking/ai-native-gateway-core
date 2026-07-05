--
-- Name: idx_credential_health_checks_credential_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_credential_created ON public.credential_health_checks USING btree (credential_id, created_at DESC);

