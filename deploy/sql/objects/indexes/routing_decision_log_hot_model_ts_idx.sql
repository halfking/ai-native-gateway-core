--
-- Name: routing_decision_log_hot_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_hot_model_ts_idx ON public.routing_decision_log_hot USING btree (model, ts DESC);

