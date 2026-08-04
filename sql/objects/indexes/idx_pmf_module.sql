--
-- Name: idx_pmf_module; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pmf_module ON public.product_module_features USING btree (module_key);

