--
-- Name: idx_envelope_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_envelope_expires ON public.request_envelope USING btree (expires_at);

