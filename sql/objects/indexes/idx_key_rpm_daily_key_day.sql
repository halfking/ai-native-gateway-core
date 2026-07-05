--
-- Name: idx_key_rpm_daily_key_day; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_rpm_daily_key_day ON public.key_rpm_daily USING btree (api_key_id, day_bucket DESC);

