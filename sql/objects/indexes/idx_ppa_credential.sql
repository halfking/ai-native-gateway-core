--
-- Name: idx_ppa_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppa_credential ON public.provider_profile_alerts USING btree (credential_id, created_at DESC);

