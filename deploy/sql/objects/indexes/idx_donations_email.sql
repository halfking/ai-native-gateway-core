--
-- Name: idx_donations_email; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_donations_email ON public.donations USING btree (lower(email));

