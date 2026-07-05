--
-- Name: idx_request_logs_model_chosen_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_model_chosen_ts ON ONLY public.request_logs USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);

