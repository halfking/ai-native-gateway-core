--
-- Name: idx_ih_instance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ih_instance ON public.instance_heartbeats USING btree (instance_id, "timestamp" DESC);

