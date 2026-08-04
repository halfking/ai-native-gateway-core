--
-- Name: idx_ppd_total_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppd_total_score ON public.provider_profile_daily USING btree (total_score);

