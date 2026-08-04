--
-- Name: routing_decision_log_hot_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_hot_request_id_idx ON public.routing_decision_log_hot USING btree (request_id);

