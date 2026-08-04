--
-- Name: uq_route_incident_events_idem; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_route_incident_events_idem ON public.route_incident_events USING btree (incident_id, request_id, terminal_status) WHERE (request_id IS NOT NULL);

