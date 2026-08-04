--
-- Name: idx_ppa_type_level; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppa_type_level ON public.provider_profile_alerts USING btree (alert_type, alert_level, created_at DESC);

