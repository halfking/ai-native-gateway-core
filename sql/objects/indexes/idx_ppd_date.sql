--
-- Name: idx_ppd_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppd_date ON public.provider_profile_daily USING btree (profile_date DESC);

