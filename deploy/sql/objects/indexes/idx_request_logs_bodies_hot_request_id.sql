--
-- Name: idx_request_logs_bodies_hot_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_request_logs_bodies_hot_request_id ON public.request_logs_bodies_hot USING btree (request_id);

