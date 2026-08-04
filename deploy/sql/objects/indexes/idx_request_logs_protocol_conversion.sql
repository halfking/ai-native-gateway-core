--
-- Name: idx_request_logs_protocol_conversion; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_protocol_conversion ON ONLY public.request_logs USING btree (protocol_conversion) WHERE (protocol_conversion = true);

