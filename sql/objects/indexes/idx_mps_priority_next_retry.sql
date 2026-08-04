--
-- Name: idx_mps_priority_next_retry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_priority_next_retry ON public.model_probe_state USING btree (probe_priority, next_retry_at) WHERE (state = ANY (ARRAY['suspicious'::text, 'failing'::text, 'recovering'::text]));

