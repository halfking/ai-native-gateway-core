--
-- Name: idx_request_logs_hot_canonical_model_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_canonical_model_ts ON public.request_logs_hot USING btree (canonical_model, ts DESC) WHERE (canonical_model IS NOT NULL);

