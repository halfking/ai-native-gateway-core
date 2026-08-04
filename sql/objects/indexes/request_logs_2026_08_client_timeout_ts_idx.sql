--
-- Name: request_logs_2026_08_client_timeout_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_client_timeout_ts_idx ON public.request_logs_2026_08 USING btree (client_timeout, ts DESC) WHERE (client_timeout = true);

