--
-- Name: idx_model_probe_state_retry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_probe_state_retry ON public.model_probe_state USING btree (state, next_retry_at) WHERE (state = 'recovering'::text);

