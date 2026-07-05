--
-- Name: idx_mps_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_due ON public.model_probe_state USING btree (next_retry_at) WHERE (state = ANY (ARRAY['unknown'::text, 'recovering'::text]));

