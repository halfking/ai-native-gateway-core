--
-- Name: idx_request_logs_session_outbound; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_session_outbound ON ONLY public.request_logs USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));

