--
-- Name: idx_request_logs_upstream_finish_reason; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_upstream_finish_reason ON ONLY public.request_logs USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));

