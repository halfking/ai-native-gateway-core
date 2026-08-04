--
-- Name: request_wal_2026_07_status_stage_idx2; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_status_stage_idx2 ON public.request_wal_2026_07 USING btree (status, stage);

