--
-- Name: idx_dae_hot_errors; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_errors ON public.dashboard_access_events_hot USING btree ("timestamp" DESC) WHERE (status_code >= 400);

