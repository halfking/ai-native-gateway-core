--
-- Name: idx_lm_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_lm_license ON public.license_modules USING btree (license_id);

