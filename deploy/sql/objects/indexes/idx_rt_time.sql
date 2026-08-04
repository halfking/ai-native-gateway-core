--
-- Name: idx_rt_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rt_time ON public.runtime_metrics USING btree ("timestamp" DESC);

