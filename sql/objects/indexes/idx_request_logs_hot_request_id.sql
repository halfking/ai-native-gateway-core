--
-- Name: idx_request_logs_hot_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_request_id ON public.request_logs_hot USING btree (request_id);

