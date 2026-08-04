--
-- Name: idx_rt_instance_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rt_instance_time ON public.runtime_metrics USING btree (instance_id, "timestamp" DESC);

