--
-- Name: idx_licenses_holder; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_licenses_holder ON public.licenses USING btree (holder_id) WHERE (holder_id IS NOT NULL);

