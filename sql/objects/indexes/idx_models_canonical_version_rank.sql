--
-- Name: idx_models_canonical_version_rank; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_version_rank ON public.models_canonical USING btree (version_rank);

