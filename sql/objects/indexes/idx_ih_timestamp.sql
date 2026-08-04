--
-- Name: idx_ih_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ih_timestamp ON public.instance_heartbeats USING btree ("timestamp" DESC);

