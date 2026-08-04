--
-- Name: idx_ppd_provider_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppd_provider_date ON public.provider_profile_daily USING btree (provider_id, profile_date DESC);

