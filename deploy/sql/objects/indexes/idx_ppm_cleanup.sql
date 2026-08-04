--
-- Name: idx_ppm_cleanup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ppm_cleanup ON public.provider_profile_metrics USING btree (created_at);

