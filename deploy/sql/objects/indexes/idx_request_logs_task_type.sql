--
-- Name: idx_request_logs_task_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_task_type ON ONLY public.request_logs USING btree (task_type);

