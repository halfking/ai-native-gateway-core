--
-- Name: idx_request_logs_auto; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_auto ON ONLY public.request_logs USING btree (is_auto_request, task_type, ts DESC);

