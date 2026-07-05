--
-- Name: idx_provider_events_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_events_credential_ts ON public.provider_events USING btree (credential_id, ts DESC);

