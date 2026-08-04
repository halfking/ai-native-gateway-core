--
-- Name: idx_dae_hot_cleanup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_cleanup ON public.dashboard_access_events_hot USING btree (created_at);

