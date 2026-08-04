--
-- Name: request_wal_hot_status_stage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_hot_status_stage_idx ON public.request_wal_hot USING btree (status, stage);

