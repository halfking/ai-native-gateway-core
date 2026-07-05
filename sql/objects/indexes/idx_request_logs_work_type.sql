--
-- Name: idx_request_logs_work_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_work_type ON ONLY public.request_logs USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));

