--
-- Name: idx_lma_module; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_lma_module ON public.license_module_audit USING btree (module_key, created_at DESC);

