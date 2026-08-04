--
-- Name: idx_dashboard_access_events_2026_08_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dashboard_access_events_2026_08_tenant ON public.dashboard_access_events_2026_08 USING btree (tenant_id, "timestamp" DESC);

