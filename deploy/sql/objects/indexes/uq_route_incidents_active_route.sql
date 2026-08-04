--
-- Name: uq_route_incidents_active_route; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_route_incidents_active_route ON public.route_incidents USING btree (tenant_id, endpoint_protocol, model, COALESCE(provider_id, (0)::bigint), COALESCE(credential_id, (0)::bigint)) WHERE (state = ANY (ARRAY['active'::text, 'recovering'::text]));

