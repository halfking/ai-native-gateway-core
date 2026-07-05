--
-- Name: idx_weekly_peak_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_weekly_peak_cred ON public.credential_model_weekly_peak USING btree (credential_id, week_start DESC);

