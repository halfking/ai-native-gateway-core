--
-- Name: idx_licenses_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_licenses_expires ON public.licenses USING btree (expires_at) WHERE (expires_at IS NOT NULL);

