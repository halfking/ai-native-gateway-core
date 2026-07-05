--
-- Name: idx_call_history_model_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_model_time ON public.credential_model_call_history USING btree (raw_model, window_start DESC);

