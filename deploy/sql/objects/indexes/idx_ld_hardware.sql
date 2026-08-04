--
-- Name: idx_ld_hardware; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ld_hardware ON public.license_devices USING btree (hardware_hash);

