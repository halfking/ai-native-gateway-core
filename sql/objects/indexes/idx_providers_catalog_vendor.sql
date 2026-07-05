--
-- Name: idx_providers_catalog_vendor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_catalog_vendor ON public.providers USING btree (catalog_code) WHERE (catalog_code IS NOT NULL);

