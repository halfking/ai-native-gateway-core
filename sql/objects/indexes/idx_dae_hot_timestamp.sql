--
-- Name: idx_dae_hot_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_timestamp ON public.dashboard_access_events_hot USING btree ("timestamp" DESC);

