--
-- Name: idx_request_logs_is_auto_request_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_is_auto_request_task_ts ON ONLY public.request_logs USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);

