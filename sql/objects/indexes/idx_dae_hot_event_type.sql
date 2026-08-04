--
-- Name: idx_dae_hot_event_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_event_type ON public.dashboard_access_events_hot USING btree (event_type, "timestamp" DESC);

