--
-- Name: idx_ppm_credential_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppm_credential_time ON public.provider_profile_metrics USING btree (credential_id, metric_time DESC);

