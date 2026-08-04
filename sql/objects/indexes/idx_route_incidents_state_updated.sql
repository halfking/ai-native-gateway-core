--
-- Name: idx_route_incidents_state_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_incidents_state_updated ON public.route_incidents USING btree (state, updated_at DESC);

