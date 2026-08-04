--
-- Name: idx_ld_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ld_status ON public.license_devices USING btree (status);

