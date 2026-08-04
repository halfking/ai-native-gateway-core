--
-- Name: idx_donations_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_donations_status ON public.donations USING btree (status, created_at DESC);

