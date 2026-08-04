--
-- Name: idx_ppd_credential_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppd_credential_date ON public.provider_profile_daily USING btree (credential_id, profile_date DESC);

