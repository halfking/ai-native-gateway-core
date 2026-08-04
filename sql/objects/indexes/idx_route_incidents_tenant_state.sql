--
-- Name: idx_route_incidents_tenant_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_incidents_tenant_state ON public.route_incidents USING btree (tenant_id, state, updated_at DESC);

