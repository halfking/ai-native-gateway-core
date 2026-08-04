--
-- Name: idx_mps_suspicious_expired; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_suspicious_expired ON public.model_probe_state USING btree (state_expires_at) WHERE ((state = ANY (ARRAY['available'::text, 'unavailable'::text])) AND (state_expires_at IS NOT NULL));

