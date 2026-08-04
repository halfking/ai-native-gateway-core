--
-- Name: idx_ppa_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppa_provider ON public.provider_profile_alerts USING btree (provider_id, created_at DESC);

