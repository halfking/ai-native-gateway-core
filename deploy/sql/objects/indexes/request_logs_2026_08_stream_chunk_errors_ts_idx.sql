--
-- Name: request_logs_2026_08_stream_chunk_errors_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_stream_chunk_errors_ts_idx ON public.request_logs_2026_08 USING btree (stream_chunk_errors, ts DESC) WHERE ((stream_chunk_errors IS NOT NULL) AND (stream_chunk_errors > 0));

