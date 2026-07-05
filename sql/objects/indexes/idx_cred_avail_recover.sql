--
-- Name: idx_cred_avail_recover; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cred_avail_recover ON public.credentials USING btree (availability_recover_at) WHERE ((availability_state = ANY (ARRAY['cooling'::text, 'rate_limited'::text, 'unreachable'::text])) AND (availability_recover_at IS NOT NULL));

