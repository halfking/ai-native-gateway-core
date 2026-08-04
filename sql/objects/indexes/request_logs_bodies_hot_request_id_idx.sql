--
-- Name: request_logs_bodies_hot_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_bodies_hot_request_id_idx ON public.request_logs_bodies_hot USING btree (request_id);

