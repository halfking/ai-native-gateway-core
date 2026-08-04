--
-- Name: routing_decision_log_hot_request_id_ts_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX routing_decision_log_hot_request_id_ts_key ON public.routing_decision_log_hot USING btree (request_id, ts);

