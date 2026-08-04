--
-- Name: idx_request_logs_customer_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_customer_id ON ONLY public.request_logs USING btree (customer_id);

