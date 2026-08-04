--
-- Name: idx_dae_hot_slow; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_slow ON public.dashboard_access_events_hot USING btree (response_time_ms DESC, "timestamp" DESC) WHERE (response_time_ms > 1000);

