--
-- Name: idx_ld_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ld_license ON public.license_devices USING btree (license_id);

