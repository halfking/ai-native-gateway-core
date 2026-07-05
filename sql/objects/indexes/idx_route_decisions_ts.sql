--
-- Name: idx_route_decisions_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_decisions_ts ON public.route_decisions USING btree (ts DESC);

