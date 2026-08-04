--
-- Name: idx_response_format_anomalies_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_response_format_anomalies_request_id ON public.response_format_anomalies USING btree (request_id);

