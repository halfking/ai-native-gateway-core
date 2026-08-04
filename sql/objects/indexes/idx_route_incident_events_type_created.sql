--
-- Name: idx_route_incident_events_type_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_incident_events_type_created ON public.route_incident_events USING btree (event_type, created_at DESC);

