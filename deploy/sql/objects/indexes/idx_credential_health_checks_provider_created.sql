--
-- Name: idx_credential_health_checks_provider_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_provider_created ON public.credential_health_checks USING btree (provider_id, created_at DESC);

