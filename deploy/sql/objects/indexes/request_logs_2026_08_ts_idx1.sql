--
-- Name: request_logs_2026_08_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_ts_idx1 ON public.request_logs_2026_08 USING btree (ts DESC) WHERE (attachments IS NOT NULL);

