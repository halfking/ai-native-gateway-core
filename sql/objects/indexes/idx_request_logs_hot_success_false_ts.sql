--
-- Name: idx_request_logs_hot_success_false_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_success_false_ts ON public.request_logs_hot USING btree (ts DESC, error_kind) WHERE (success = false);

