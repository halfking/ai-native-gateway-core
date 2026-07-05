--
-- Name: idx_routing_overrides_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_expires ON public.routing_overrides USING btree (expires_at) WHERE (expires_at IS NOT NULL);

