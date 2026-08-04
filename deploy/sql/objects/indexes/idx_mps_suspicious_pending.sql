--
-- Name: idx_mps_suspicious_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_suspicious_pending ON public.model_probe_state USING btree (marked_suspicious_at, next_retry_at) WHERE (state = 'suspicious'::text);

