--
-- Name: idx_gi_heartbeat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_heartbeat ON public.gateway_instances USING btree (last_heartbeat DESC);

