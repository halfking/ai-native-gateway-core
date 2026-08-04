--
-- Name: idx_dae_hot_api_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_api_time ON public.dashboard_access_events_hot USING btree (api_path, "timestamp" DESC);

