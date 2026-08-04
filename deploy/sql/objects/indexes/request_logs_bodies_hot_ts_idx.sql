--
-- Name: request_logs_bodies_hot_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_bodies_hot_ts_idx ON public.request_logs_bodies_hot USING btree (ts DESC);

