--
-- Name: idx_models_canonical_family_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_family_status ON public.models_canonical USING btree (family, status);

