--
-- Name: ursm_node_snapshot_min_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ursm_node_snapshot_min_ts_idx ON public.ursm_node_snapshot_min USING btree (snapshot_ts);

