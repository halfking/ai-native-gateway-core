--
-- Name: idx_call_history_errors; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_errors ON public.credential_model_call_history USING btree (credential_id, raw_model, window_start DESC) WHERE ((error_rate_limit_count > 0) OR (error_concurrent_count > 0));

