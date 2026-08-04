--
-- Name: idx_ppm_provider_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppm_provider_time ON public.provider_profile_metrics USING btree (provider_id, metric_time DESC);

