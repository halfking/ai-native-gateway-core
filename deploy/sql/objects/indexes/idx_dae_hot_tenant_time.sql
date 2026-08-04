--
-- Name: idx_dae_hot_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dae_hot_tenant_time ON public.dashboard_access_events_hot USING btree (tenant_id, "timestamp" DESC);

