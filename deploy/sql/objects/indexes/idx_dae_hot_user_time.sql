--
-- Name: idx_dae_hot_user_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_user_time ON public.dashboard_access_events_hot USING btree (user_id, "timestamp" DESC) WHERE (user_id IS NOT NULL);

