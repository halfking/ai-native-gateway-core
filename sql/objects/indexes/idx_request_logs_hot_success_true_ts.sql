--
-- Name: idx_request_logs_hot_success_true_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_success_true_ts ON public.request_logs_hot USING btree (ts DESC) WHERE (success = true);

