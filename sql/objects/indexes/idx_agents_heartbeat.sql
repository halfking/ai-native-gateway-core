--
-- Name: idx_agents_heartbeat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_heartbeat ON public.agents USING btree (last_heartbeat) WHERE (last_heartbeat IS NOT NULL);

