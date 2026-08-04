--
-- Name: idx_lma_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_lma_key ON public.license_module_audit USING btree (license_key, created_at DESC);

