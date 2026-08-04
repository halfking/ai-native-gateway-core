--
-- Name: idx_route_incident_events_incident_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_incident_events_incident_created ON public.route_incident_events USING btree (incident_id, created_at DESC);

